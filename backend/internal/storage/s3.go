// Package storage — доступ к объектному хранилищу (Cloudflare R2, S3-API,
// ADR-009). Бакет приватный: наружу уходят только presigned-ссылки, сам
// файл идёт хранилище ↔ браузер, минуя VPS.
package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// PresignTTL — срок жизни выданной ссылки (05-storage-s3.md). Достаточно,
// чтобы досмотреть урок, и мало, чтобы ссылка ушла в переписку надолго.
const PresignTTL = 30 * time.Minute

var (
	ErrNotConfigured  = errors.New("storage: ключи хранилища не заданы")
	ErrNoRegion       = errors.New("storage: без endpoint (AWS S3) нужен регион")
	ErrObjectNotFound = errors.New("storage: объекта нет")
)

type Config struct {
	Bucket    string
	Region    string
	Endpoint  string // пусто для AWS; для R2 — https://<account>.r2.cloudflarestorage.com
	AccessKey string
	SecretKey string
}

// ConfigFromEnv — ключи S3_* из deploy/.env; читают и api, и worker.
func ConfigFromEnv() Config {
	return Config{
		Bucket:    os.Getenv("S3_BUCKET"),
		Region:    os.Getenv("S3_REGION"),
		Endpoint:  os.Getenv("S3_ENDPOINT"),
		AccessKey: os.Getenv("S3_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
	}
}

func (c Config) configured() bool {
	return c.Bucket != "" && c.AccessKey != "" && c.SecretKey != ""
}

type Client struct {
	cfg      Config
	region   string
	presign  *s3.PresignClient
	s3client *s3.Client
}

// resolveRegion — регион подписи. auto понимает только R2 (задан endpoint),
// там пустой регион и означает auto (ADR-009). Для AWS SDK подставил бы auto
// в адрес и в подпись запроса, и ссылки молча не работали бы, поэтому без
// endpoint нужен настоящий регион — пустой и auto отвергаются.
func resolveRegion(cfg Config) (string, error) {
	if cfg.Endpoint == "" {
		if cfg.Region == "" || cfg.Region == "auto" {
			return "", ErrNoRegion
		}
		return cfg.Region, nil
	}
	if cfg.Region == "" {
		return "auto", nil
	}
	return cfg.Region, nil
}

// New возвращает nil без ошибки, если хранилище не настроено: стенд должен
// подниматься и без ключей, а эндпоинты записи отвечать 503.
func New(cfg Config) (*Client, error) {
	if !cfg.configured() {
		return nil, nil
	}
	region, err := resolveRegion(cfg)
	if err != nil {
		return nil, err
	}
	opts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			// R2 не поддерживает virtual-host стиль для произвольных бакетов
			o.UsePathStyle = true
		})
	}
	client := s3.New(s3.Options{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
	}, opts...)

	return &Client{cfg: cfg, region: region, presign: s3.NewPresignClient(client), s3client: client}, nil
}

// Region — регион, которым подписаны запросы. Его же получает Egress:
// запись пишется в тот же бакет тем же токеном.
func (c *Client) Region() string { return c.region }

// PresignGet — временная ссылка на скачивание. Хранилище отдаёт Range,
// поэтому перемотка в плеере работает без дополнительной механики.
func (c *Client) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if c == nil {
		return "", ErrNotConfigured
	}
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign get %s: %w", key, err)
	}
	return req.URL, nil
}

// ObjectInfo — то, что хранилище знает об объекте, без его содержимого.
type ObjectInfo struct {
	Size        int64
	ContentType string
	// ETag меняется с каждой перезаписью объекта: по нему api видит
	// подмену подтверждённого файла (ADR-024)
	ETag string
}

// Stat — размер, тип и ETag объекта; ErrObjectNotFound, если его нет. Им
// api сверяет загруженный браузером файл с заявленным (ADR-024).
func (c *Client) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if c == nil {
		return ObjectInfo{}, ErrNotConfigured
	}
	out, err := c.s3client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return ObjectInfo{}, ErrObjectNotFound
		}
		// У HEAD тела нет, поэтому SDK не всегда распознаёт тип ошибки —
		// подстраховываемся кодом. R2 отвечает NoSuchKey.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "NotFound", "NoSuchKey":
				return ObjectInfo{}, ErrObjectNotFound
			}
		}
		return ObjectInfo{}, fmt.Errorf("head %s: %w", key, err)
	}
	return ObjectInfo{
		Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType),
		ETag: aws.ToString(out.ETag),
	}, nil
}

// Copy — серверное копирование объекта внутри бакета: байты не идут через
// VPS, тип и Content-Disposition копия берёт у источника. Так api переносит
// подтверждённый файл материала из materials-pending/ на постоянный ключ.
func (c *Client) Copy(ctx context.Context, src, dst string) error {
	if c == nil {
		return ErrNotConfigured
	}
	if _, err := c.s3client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		// Ключи выбирает api из [0-9a-z/.-], экранировать в них нечего
		CopySource: aws.String(c.cfg.Bucket + "/" + src),
		Key:        aws.String(dst),
	}); err != nil {
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	return nil
}

// PresignedPut — ссылка на загрузку и заголовки, которые браузер обязан
// прислать ровно такими: они вошли в подпись.
type PresignedPut struct {
	URL    string
	Header http.Header
}

// PresignPut — временная ссылка на загрузку объекта браузером напрямую
// в хранилище. Тип, размер и Content-Disposition входят в подпись:
// другой тип или длина тела дают 403 SignatureDoesNotMatch, а заданное
// имя файла хранилище потом отдаёт при скачивании.
func (c *Client) PresignPut(ctx context.Context, key, contentType, contentDisposition string, size int64, ttl time.Duration) (PresignedPut, error) {
	if c == nil {
		return PresignedPut{}, ErrNotConfigured
	}
	req, err := c.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:             aws.String(c.cfg.Bucket),
		Key:                aws.String(key),
		ContentType:        aws.String(contentType),
		ContentLength:      aws.Int64(size),
		ContentDisposition: aws.String(contentDisposition),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedPut{}, fmt.Errorf("presign put %s: %w", key, err)
	}
	header := req.SignedHeader.Clone()
	// Host и Content-Length браузер выставляет сам и задать их не даст
	header.Del("Host")
	header.Del("Content-Length")
	return PresignedPut{URL: req.URL, Header: header}, nil
}

// Delete — удалить объект. Удаление отсутствующего ключа — не ошибка
// (так отвечает S3 API), поэтому повтор безопасен.
func (c *Client) Delete(ctx context.Context, key string) error {
	if c == nil {
		return ErrNotConfigured
	}
	if _, err := c.s3client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

// NewRecordingKey — ключ сегмента записи урока (05-storage-s3.md, ADR-031):
// recordings/{lesson_id}/<время UTC>-<случайные 64 бита>.<ext>. У каждого
// запуска Egress свой файл: повторный запуск или переподключение после
// опустевшей комнаты не перезаписывают уже сделанную запись.
func NewRecordingKey(lessonID int64, ext string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "recordings/" + strconv.FormatInt(lessonID, 10) + "/" +
		time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b) + "." + ext, nil
}

// NewMaterialKey — ключ файла материала: materials/{lesson_id}/<случайные
// 128 бит>.<ext> (05-storage-s3.md). Имя файла от клиента в ключ не
// попадает: путь целиком выбирает api.
func NewMaterialKey(lessonID int64, ext string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return materialPrefix + strconv.FormatInt(lessonID, 10) + "/" + hex.EncodeToString(b) + "." + ext, nil
}

// PendingMaterialKey — куда браузер грузит файл материала до подтверждения:
// materials-pending/{lesson_id}/<имя ключа>. Lifecycle бакета стирает этот
// префикс через сутки (deploy/s3-bootstrap.sh), поэтому загрузка, которая
// пришла после удаления материала или отмены урока, сиротой не останется.
func PendingMaterialKey(key string) string {
	return materialPendingPrefix + strings.TrimPrefix(key, materialPrefix)
}

// MaterialFileKeys — все объекты файла материала: загрузка под
// materials-pending/ и постоянный ключ. Удаляя файл, удаляют оба.
func MaterialFileKeys(key string) []string {
	return []string{PendingMaterialKey(key), key}
}

const (
	materialPrefix        = "materials/"
	materialPendingPrefix = "materials-pending/"
)

// TranscriptKey — путь VTT-транскрипта урока (05-storage-s3.md).
func TranscriptKey(lessonID int64) string {
	return "transcripts/" + strconv.FormatInt(lessonID, 10) + ".vtt"
}

// PutObject кладёт небольшой объект целиком: VTT транскрипта (S2-3)
// и снимок доски (S3-6) — килобайты текста, а не медиа, которое пишет
// Egress мимо VPS. Единственная запись объекта из api и worker.
func (c *Client) PutObject(ctx context.Context, key, contentType string, body []byte) error {
	if c == nil {
		return ErrNotConfigured
	}
	_, err := c.s3client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.cfg.Bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
		ContentType:   aws.String(contentType),
	}, func(o *s3.Options) {
		// Без необязательной контрольной суммы SDK шлёт обычный подписанный
		// PUT, а не aws-chunked с трейлером (так он делает по HTTPS
		// в режиме WhenSupported — умолчании LoadDefaultConfig): такой
		// запрос одинаково понимают R2, AWS и S3-совместимые заглушки
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// WhiteboardKey — снимок сцены доски урока (05-storage-s3.md).
func WhiteboardKey(lessonID int64) string {
	return "whiteboards/" + strconv.FormatInt(lessonID, 10) + ".json"
}
