package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"openfox/internal/daemon/commands"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/runtime/codex"
	"openfox/internal/skillpack"
)

// maxImportNames 是一次 skill_import 的名称数量上限（与中心一致）。
const maxImportNames = 20

// skillUploader 是 skill_import 使用的中心端点（supervisor.Client 实现）。
type skillUploader interface {
	UploadImportedSkill(ctx context.Context, organizationID, commandID string, bundle supervisor.SkillBundle) (supervisor.SkillImportItem, error)
}

type skillScanResult struct {
	Supported bool                 `json:"supported"`
	Runtime   string               `json:"runtime,omitempty"`
	Skills    []skillpack.ScanItem `json:"skills"`
}

type skillImportResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
	Failed   int `json:"failed"`
}

// skillDirs 返回要扫描的技能目录（优先顺序：~/.agents/skills，然后 $CODEX_HOME/skills）。
func (h *commandHandlers) skillDirs() ([]string, error) {
	if h.userSkillDirs == nil {
		return nil, errors.New("skill directories are unknown")
	}
	return h.userSkillDirs()
}

// skillScan 扫描本机 Codex 技能目录。本机没有可用的 Codex 适配器时 supported = false 且 skills 为空。
func (h *commandHandlers) skillScan(ctx context.Context, params json.RawMessage) (any, error) {
	if err := emptyParams(params); err != nil {
		return nil, err
	}
	empty := skillScanResult{Skills: []skillpack.ScanItem{}}
	if _, err := h.reporter.prober.Detect(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return empty, nil
	}
	dirs, err := h.skillDirs()
	if err != nil {
		return skillScanResult{Supported: true, Runtime: codex.RuntimeName, Skills: []skillpack.ScanItem{}}, nil
	}
	items, err := skillpack.Scan(dirs)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &commands.CodedError{Code: "SCAN_FAILED"}
	}
	return skillScanResult{Supported: true, Runtime: codex.RuntimeName, Skills: items}, nil
}

// skillImport 按 names 顺序逐个打包本机技能并上传；收到 409 IMPORT_CANCELED 立即停止。
func (h *commandHandlers) skillImport(ctx context.Context, params json.RawMessage) (any, error) {
	var input struct {
		SpaceID string   `json:"space_id"`
		Names   []string `json:"names"`
	}
	if err := decodeParams(params, &input); err != nil {
		return nil, err
	}
	if input.SpaceID == "" || len(input.Names) == 0 || len(input.Names) > maxImportNames {
		return nil, commands.ErrInvalidParams
	}
	organizationID, commandID, ok := commands.FromContext(ctx)
	if !ok || h.uploader == nil {
		return nil, errors.New("skill_import: command context is unavailable")
	}
	dirs, dirErr := h.skillDirs()
	var result skillImportResult
	seen := map[string]struct{}{}
	for _, name := range input.Names {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bundle := supervisor.SkillBundle{Name: name, Files: []supervisor.SkillBundleFile{}}
		if dirErr != nil {
			bundle.ErrorCode = skillpack.CodeNotFound
		} else if files, err := skillpack.Pack(dirs, name); err != nil {
			var packErr *skillpack.PackError
			if !errors.As(err, &packErr) {
				return nil, err
			}
			bundle.ErrorCode = packErr.Code
		} else {
			for _, file := range files {
				bundle.Files = append(bundle.Files, supervisor.SkillBundleFile{Path: file.Path, ContentBase64: base64.StdEncoding.EncodeToString(file.Content)})
			}
		}
		item, err := h.uploader.UploadImportedSkill(ctx, organizationID, commandID, bundle)
		if err != nil {
			switch {
			case supervisor.IsAPIError(err, http.StatusConflict, "IMPORT_CANCELED"):
				return result, nil
			case ctx.Err() != nil:
				return nil, ctx.Err()
			case supervisor.IsAPIError(err, http.StatusRequestEntityTooLarge, ""):
				slog.Warn("skill_import_upload_rejected", "name", name, "error", err.Error())
				// 中心拒绝过大的请求体：该项算失败，继续下一个。
				result.Failed++
				continue
			case supervisor.Permanent(err):
				// 命令不属于本设备 / 已结束等：继续上传没有意义。
				return nil, &commands.CodedError{Code: "UPLOAD_REJECTED", Result: result}
			}
			slog.Warn("skill_import_upload_failed", "name", name, "error", err.Error())
			result.Failed++
			continue
		}
		switch item.Status {
		case "imported":
			result.Imported++
		case "skipped":
			result.Skipped++
		case "failed":
			result.Failed++
		}
	}
	return result, nil
}
