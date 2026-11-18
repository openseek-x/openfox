package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifacts"
	"openfox/internal/tenancy"
)

// 文件传输按 64 KiB/s 预留时间，另给 30 秒处理余量；单次最多 30 分钟。
// 上传同时延长响应截止时间，避免文件读完后无法写回结果。普通接口仍使用服务端的 15 秒。
func setFileTransferDeadline(w http.ResponseWriter, size int64, upload bool) {
	size = max(0, min(size, 1<<30))
	duration := min(30*time.Second+time.Duration((size+(64<<10)-1)/(64<<10))*time.Second, 30*time.Minute)
	deadline := time.Now().Add(duration)
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(deadline)
	if upload {
		_ = controller.SetReadDeadline(deadline)
	}
}

// 请求已经结束也尽力回收孤立对象，保留上下文值并限制清理总时间。
func removeStorageObjects(ctx context.Context, storage artifacts.Storage, keys ...string) {
	if storage == nil {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, key := range keys {
		if cleanup.Err() != nil {
			return
		}
		_ = storage.Delete(cleanup, key)
	}
}

func removeUnregisteredStorageObject(ctx context.Context, storage artifacts.Storage, key string, registered func(context.Context) (bool, error)) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	exists, err := registered(cleanup)
	if err == nil && !exists {
		_ = storage.Delete(cleanup, key)
	}
}

// 等待持有运行行锁的登记事务结束，再用 READ COMMITTED 的新语句核查。
func artifactStorageRegistered(ctx context.Context, tx pgx.Tx, runID, key string) (bool, error) {
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.runs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, runID).Scan(&locked); err != nil {
		return false, err
	}
	var registered bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.artifacts WHERE organization_id=openfox_private.current_organization_id() AND storage_key=$1) OR EXISTS(SELECT 1 FROM openfox.workspace_proposals WHERE organization_id=openfox_private.current_organization_id() AND storage_key=$1)`, key).Scan(&registered)
	return registered, err
}

// An acknowledged rollback can be cleaned up immediately. After a lost COMMIT
// response, synchronize with the same parent lock used by the upload before
// reading the registration in a fresh READ COMMITTED statement.
func (h *Handler) cleanupFailedAttachment(r *http.Request, selection tenancy.OrganizationSelection, parentTable, parentID, attachmentID, key string, uploadErr error) {
	if !errors.Is(uploadErr, tenancy.ErrCommitTransaction) {
		removeStorageObjects(r.Context(), h.delivery.Storage, key)
		return
	}
	table := "attachments"
	if parentTable == "direct_chats" {
		table = "direct_attachments"
	}
	removeUnregisteredStorageObject(r.Context(), h.delivery.Storage, key, func(ctx context.Context) (bool, error) {
		var registered bool
		err := h.runConversationTransaction(r.WithContext(ctx), selection, func(ctx context.Context, tx pgx.Tx) error {
			var locked int
			if err := tx.QueryRow(ctx, `SELECT 1 FROM `+pgx.Identifier{"openfox", parentTable}.Sanitize()+`
				WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, parentID).Scan(&locked); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+pgx.Identifier{"openfox", table}.Sanitize()+`
				WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND file_key=$2)`, attachmentID, key).Scan(&registered)
		})
		return registered, err
	})
}
