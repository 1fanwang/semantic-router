package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAdminMutationRevalidatesActorInsideWriteTransaction(t *testing.T) {
	for _, test := range []struct {
		operation string
		revoke    string
	}{
		{"role", "permission"}, {"role", "session"},
		{"delete", "permission"}, {"delete", "session"},
		{"password", "permission"}, {"password", "session"},
	} {
		t.Run(test.operation+"/"+test.revoke, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.db")
			store, err := NewStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			revoker, err := NewStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = revoker.Close() })
			svc := NewService(store, "admin-mutation-transaction-secret", 1)
			ctx := context.Background()
			if err := svc.EnsureBootstrapAdmin(ctx, "admin@example.test", "test-password", "Admin"); err != nil {
				t.Fatal(err)
			}
			token, admin, err := svc.Login(ctx, "admin@example.test", "test-password")
			if err != nil {
				t.Fatal(err)
			}
			claims, err := svc.ParseToken(token)
			if err != nil {
				t.Fatal(err)
			}
			target, err := store.CreateUser(ctx, "target@example.test", "Target", "old-hash", RoleRead, "active")
			if err != nil {
				t.Fatal(err)
			}
			actor := AuthContext{UserID: admin.ID, SessionID: claims.ID}

			// Keep the first store's only connection occupied until an independent
			// connection has committed the revocation. The admitted mutation then
			// proceeds and must observe it in its own write transaction.
			hold, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = hold.Rollback() }()
			waitBefore := store.db.Stats().WaitCount
			finished := make(chan error, 1)
			go func() {
				switch test.operation {
				case "role":
					_, err := store.UpdateUserRoleOrStatusAuthorized(ctx, actor, target.ID, RoleWrite, "active")
					finished <- err
				case "delete":
					finished <- store.DeleteUserAuthorized(ctx, actor, target.ID)
				case "password":
					finished <- store.UpdatePasswordAuthorized(ctx, actor, target.ID, "new-hash")
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for store.db.Stats().WaitCount == waitBefore && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if store.db.Stats().WaitCount == waitBefore {
				t.Fatal("admin mutation did not wait for the held connection")
			}
			if test.revoke == "session" {
				err = revoker.RevokeSession(ctx, claims.ID)
			} else {
				_, err = revoker.UpdateUserRoleOrStatus(ctx, admin.ID, RoleRead, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := hold.Rollback(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				want := ErrPermissionDenied
				if test.revoke == "session" {
					want = errAdminSessionInvalid
				}
				if !errors.Is(err, want) {
					t.Fatalf("mutation after revocation = %v, want %v", err, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("admin mutation did not finish")
			}
			stored, err := store.GetUserByID(ctx, target.ID)
			if err != nil || stored.Role != RoleRead || stored.Status != "active" {
				t.Fatalf("revoked account mutation changed target: %+v, %v", stored, err)
			}
			var hash string
			if err := store.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, target.ID).Scan(&hash); err != nil || hash != "old-hash" {
				t.Fatalf("revoked password mutation changed hash: %q, %v", hash, err)
			}
		})
	}
}
