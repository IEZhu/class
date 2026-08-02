package httpapi

import (
	"testing"

	"github.com/livekit/protocol/auth"

	"github.com/IEZhu/class/backend/internal/store"
)

// Токен подписывается ключами LiveKit и проверяется тем же SDK, что и на
// стороне сервера комнат — тест ловит расхождение грантов до стенда.
func TestRoomTokenGrants(t *testing.T) {
	const (
		apiKey    = "APItestkey"
		apiSecret = "test-secret-at-least-32-bytes-long-x"
	)
	u := &store.User{ID: 42, Name: "Тест Учитель", Role: store.RoleTeacher}

	jwt, err := roomToken(apiKey, apiSecret, roomName(7), u)
	if err != nil {
		t.Fatalf("roomToken: %v", err)
	}

	v, err := auth.ParseAPIToken(jwt)
	if err != nil {
		t.Fatalf("ParseAPIToken: %v", err)
	}
	if got := v.APIKey(); got != apiKey {
		t.Errorf("APIKey = %q, хотели %q", got, apiKey)
	}
	_, claims, err := v.Verify(apiSecret)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Identity != "42" {
		t.Errorf("identity = %q, хотели \"42\" (user_id)", claims.Identity)
	}
	if claims.Name != u.Name {
		t.Errorf("name = %q, хотели %q", claims.Name, u.Name)
	}
	if claims.Video == nil {
		t.Fatal("video grant отсутствует")
	}
	if !claims.Video.RoomJoin {
		t.Error("RoomJoin = false, вход в комнату не разрешён")
	}
	if claims.Video.Room != "lesson-7" {
		t.Errorf("room = %q, хотели \"lesson-7\"", claims.Video.Room)
	}
	if claims.Video.CanPublish == nil || !*claims.Video.CanPublish {
		t.Error("CanPublish не разрешён — участник не сможет говорить")
	}
	if claims.Video.CanSubscribe == nil || !*claims.Video.CanSubscribe {
		t.Error("CanSubscribe не разрешён — участник не увидит других")
	}
	// Гранты администратора комнаты никому не выдаём: иначе участник смог бы
	// выкидывать других и менять комнату.
	if claims.Video.RoomAdmin || claims.Video.RoomCreate || claims.Video.RoomList {
		t.Error("выданы административные гранты комнаты")
	}
}

// Чужим ключом токен проверяться не должен.
func TestRoomTokenRejectsWrongSecret(t *testing.T) {
	jwt, err := roomToken("APIkey", "correct-secret-at-least-32-bytes-long", roomName(1),
		&store.User{ID: 1, Name: "X"})
	if err != nil {
		t.Fatalf("roomToken: %v", err)
	}
	v, err := auth.ParseAPIToken(jwt)
	if err != nil {
		t.Fatalf("ParseAPIToken: %v", err)
	}
	if _, _, err := v.Verify("wrong-secret-at-least-32-bytes-longer"); err == nil {
		t.Error("токен принят с чужим секретом")
	}
}
