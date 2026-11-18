package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/resourcepolicy"
)

// getDeviceRuntimeConfig 供 daemon 拉取本组织运行配置（阶段 12e，K-02），之后调用既有的 ack 接口确认已应用的版本。
// 鉴权与 ack 相同：设备会话 + 这台电脑在该组织的 ACTIVE 授权；其他组织的电脑读不到。
func (h *Handler) getDeviceRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	_, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var config resourcepolicy.Config
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		config, err = h.resources.GetConfig(ctx, tx, selection.OrganizationID)
		return err
	})
	if err != nil {
		writeResourcePolicyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}
