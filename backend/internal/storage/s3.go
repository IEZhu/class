// Package storage — доступ к объектному хранилищу (Cloudflare R2, S3-API,
// ADR-009). Бакет приватный: наружу уходят только presigned-ссылки, сам
// файл идёт хранилище ↔ браузер, минуя VPS.
package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

var ErrNotConfigured = errors.New("storage: ключи хранилища не заданы")

type Config struct {
	Bucket    string
	Region    string
	Endpoint  string // пусто для AWS; для R2 — https://<account>.r2.cloudflarestorage.com
	AccessKey string
	SecretKey string
}

func (c Config) configured() bool {
	return c.Bucket != "" && c.AccessKey != "" && c.SecretKey != ""
}

type Client struct {
	cfg      Config
	presign  *s3.PresignClient
	s3client *s3.Client
}

// New возвращает nil без ошибки, если хранилище не настроено: стенд должен
// подниматься и без ключей, а эндпоинты записи отвечать 503.
func New(cfg Config) (*Client, error) {
	if !cfg.configured() {
		return nil, nil
	}
	region := cfg.Region
	if region == "" {
		region = "auto" // у R2 регион всегда auto (ADR-009)
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

	return &Client{cfg: cfg, presign: s3.NewPresignClient(client), s3client: client}, nil
}

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

// Exists — есть ли объект. Нужен, чтобы не выдавать ссылку на запись,
// которую Egress ещё не дописал.
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	if c == nil {
		return false, ErrNotConfigured
	}
	_, err := c.s3client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return false, nil
		}
		// У HEAD тела нет, поэтому SDK не всегда распознаёт тип ошибки —
		// подстраховываемся кодом. R2 отвечает NoSuchKey.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "NotFound", "NoSuchKey":
				return false, nil
			}
		}
		return false, fmt.Errorf("head %s: %w", key, err)
	}
	return true, nil
}

func (c *Client) Bucket() string { return c.cfg.Bucket }

// RecordingKey — путь записи урока по раскладке из 05-storage-s3.md.
func RecordingKey(lessonID int64, ext string) string {
	return "recordings/" + strconv.FormatInt(lessonID, 10) + "/room." + ext
}
