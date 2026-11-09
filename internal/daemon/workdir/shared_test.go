package workdir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedTaskWorkspacePersistsAndIsolates(t *testing.T) {
	for _, mode := range []Mode{ModeNoRepo, ModeSpecifiedGit} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			policy := GuardPolicy{ManagedRoot: filepath.Join(root, "managed"), AllowNonGit: true}
			manager, err := NewManager(policy)
			if err != nil {
				t.Fatal(err)
			}
			req := Request{Shared: true, Mode: mode, Key: BindingKey{OrganizationID: "org", SpaceID: "space", AgentID: "crew:one", ConversationID: "task:one"}}
			if mode == ModeSpecifiedGit {
				req.SourcePath = initRepository(t, root)
			}
			first, err := manager.Prepare(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(first.Path, "local-edit.txt"), []byte("preserved"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Prepare(context.Background(), req); !errors.Is(err, ErrPathConflict) {
				t.Fatalf("concurrent reuse = %v", err)
			}
			if err := first.Release(); err != nil {
				t.Fatal(err)
			}
			for _, restart := range []bool{false, true} {
				if restart {
					manager, err = NewManager(policy)
					if err != nil {
						t.Fatal(err)
					}
				}
				next, err := manager.Prepare(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(next.Path, "local-edit.txt"))
				if next.Path != first.Path || err != nil || string(data) != "preserved" {
					t.Fatalf("restart=%v lost shared edits: %v", restart, err)
				}
				_ = next.Release()
			}
			for _, change := range []string{"task", "crew", "organization", "disabled"} {
				other := req
				switch change {
				case "task":
					other.Key.ConversationID = "task:two"
				case "crew":
					other.Key.AgentID = "crew:two"
				case "organization":
					other.Key.OrganizationID = "other"
				case "disabled":
					other.Shared = false
					other.Key.AgentID = "member"
					other.Key.ConversationID = "conversation"
				}
				separate, err := manager.Prepare(context.Background(), other)
				if err != nil {
					t.Fatal(err)
				}
				if separate.Path == first.Path {
					t.Fatalf("%s reused another scope", change)
				}
				if _, err := os.Stat(filepath.Join(separate.Path, "local-edit.txt")); !os.IsNotExist(err) {
					t.Fatalf("%s leaked task edits: %v", change, err)
				}
				_ = separate.Release()
			}
			wrong := req
			if mode == ModeSpecifiedGit {
				wrong.SourcePath = initRepository(t, t.TempDir())
			} else {
				wrong.Mode = ModeSpecifiedGit
				wrong.SourcePath = initRepository(t, t.TempDir())
			}
			if _, err := manager.Prepare(context.Background(), wrong); !errors.Is(err, ErrBindingConflict) {
				t.Fatalf("incompatible source = %v", err)
			}
		})
	}
}
