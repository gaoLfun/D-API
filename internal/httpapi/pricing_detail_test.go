package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/config"
	"github.com/gaoLfun/dapi/internal/store"
)

func TestPricingDetailRequiresAdminAndSelectsOneProfile(t *testing.T) {
	db := readonlyDB(t)
	ctx := context.Background()
	ids := make([]int64, 2)
	for i := range ids {
		id, err := db.SavePricingProfile(ctx, store.PricingProfile{Name: fmt.Sprintf("detail-%d", i), Prices: []store.PricingModelPrice{{Model: fmt.Sprintf("model-%d", i), InputUSDPerMillion: float64(i + 1)}}})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	admin, err := db.CreateAdmin(ctx, "detail-admin", []byte("unused"))
	if err != nil {
		t.Fatal(err)
	}
	session, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CreateSession(ctx, hash, admin, "127.0.0.1", "test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(db, config.Config{}, nil, nil).Register(mux)
	for _, tc := range []struct {
		id            int64
		authenticated bool
		status        int
	}{{ids[0], false, 401}, {ids[0], true, 200}, {ids[1], true, 200}, {999999, true, 404}} {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/admin/pricing/profiles/%d", tc.id), nil)
		if tc.authenticated {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("id=%d status=%d body=%s", tc.id, w.Code, w.Body.String())
		}
		if tc.status == 200 {
			var p store.PricingProfile
			if err = json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			index := 0
			if tc.id == ids[1] {
				index = 1
			}
			if p.ID != tc.id || len(p.Prices) != 1 || p.ModelCount != 1 || p.Prices[0].Model != fmt.Sprintf("model-%d", index) {
				t.Fatalf("wrong detail: %+v", p)
			}
		}
	}
}
