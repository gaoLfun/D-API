package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/cryptox"
	"github.com/gaoLfun/dapi/internal/store"
)

func TestDisableKeyAfterLastUpstreamDeleted(t *testing.T) {
	dsn := os.Getenv("DAPI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DAPI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	schema := fmt.Sprintf("keys_test_%d", time.Now().UnixNano())
	if _, err := adminDB.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer adminDB.Exec("DROP SCHEMA " + schema + " CASCADE")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	box, err := cryptox.NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, u.String(), box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	adminID, err := db.CreateAdmin(ctx, "tester", []byte("test-hash"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateUpstream(ctx, core.Upstream{Name: "test", Kind: "newapi", BaseURL: "https://example.com", APIKey: "test", Enabled: true, Protocols: []string{"chat"}, Models: []string{}, FailureThreshold: 2, Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	groupID, err := db.CreateGroup(ctx, core.Group{Name: "test", Enabled: true, UpstreamIDs: []int64{id}})
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := db.InsertAPIKeyInGroup(ctx, "test", "test", auth.HashToken("test-client"), groupID, []string{"chat"}, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUpstream(ctx, id); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db}
	for _, enabled := range []bool{false, true} {
		body := fmt.Sprintf(`{"name":"test","enabled":%t,"group_id":%d,"protocols":["chat"],"models":[]}`, enabled, groupID)
		r := httptest.NewRequest("PUT", "/api/admin/keys/"+strconv.FormatInt(keyID, 10), strings.NewReader(body))
		r.SetPathValue("id", strconv.FormatInt(keyID, 10))
		r = r.WithContext(context.WithValue(ctx, adminContextKey{}, store.Admin{ID: adminID}))
		w := httptest.NewRecorder()
		server.updateKey(w, r)
		want := 200
		if enabled {
			want = 400
		}
		if w.Code != want {
			t.Fatalf("enabled=%v status=%d body=%s", enabled, w.Code, w.Body.String())
		}
	}
	key, err := db.APIKey(ctx, keyID)
	if err != nil || key.Enabled {
		t.Fatalf("key remained enabled: %+v %v", key, err)
	}
}
