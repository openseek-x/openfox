package workdir

import (
	"path/filepath"
	"runtime"
	"strings"
)

// ProtectedDirectories 是本机受保护目录名单（阶段 12e，D-04）。
//
//   - Subtrees：目录本身、其下任意子目录、以及包含它的上级目录都受保护
//     （选中上级目录等于把受保护目录整体交给 Run 读写）。
//   - Exact：只保护目录本身；其下的子目录仍可使用（例如外接磁盘挂载点 /Volumes）。
//   - FoldCase：按大小写不敏感比较（macOS 默认 APFS 大小写不敏感，/USERS/x/.SSH 与 /Users/x/.ssh 是同一目录）。
//
// 路径比较前一律经 canonicalPath 解析符号链接，防止用符号链接绕过。
type ProtectedDirectories struct {
	Subtrees []string
	Exact    []string
	FoldCase bool
}

// DefaultProtectedDirectories 返回内置名单。home 为本机用户主目录（空串时只含系统目录），
// configDir 为 OpenFox 本机配置目录（含设备私钥、会话令牌与本地记录；空串时跳过）。
// 名单刻意不含 /tmp、/private/tmp、/private/var/folders、/opt、/usr/local 之外的常见项目位置，
// 也不含 ~/Documents、~/Desktop、~/code 等，避免误伤常见的项目目录。
func DefaultProtectedDirectories(home, configDir, goos string) ProtectedDirectories {
	if goos == "" {
		goos = runtime.GOOS
	}
	list := ProtectedDirectories{FoldCase: goos == "darwin"}
	// 系统目录：操作系统与系统级配置，Run 不应读写。
	list.Subtrees = append(list.Subtrees,
		"/System", "/Library", "/Applications", "/bin", "/sbin", "/usr", "/etc", "/dev",
		"/private/etc", "/private/var/root", "/private/var/db", "/boot", "/proc", "/sys", "/root",
	)
	// 文件系统根与挂载点本身：选中它们等于交出整台电脑或整块磁盘。
	list.Exact = append(list.Exact, "/", "/Users", "/home", "/Volumes", "/private", "/private/var")
	if strings.TrimSpace(home) != "" && filepath.IsAbs(home) {
		home = filepath.Clean(home)
		// 用户主目录根本身：其上级关系由下面的子树名单自动覆盖（主目录包含 ~/.ssh），这里显式列出便于说明。
		list.Exact = append(list.Exact, home,
			filepath.Join(home, "Library"), filepath.Join(home, "Library", "Application Support"))
		// 凭据与密钥目录：SSH、GPG、系统钥匙串、浏览器 Cookie、云厂商与容器/集群凭据、云盘同步工具凭据、密码库。
		for _, relative := range []string{
			".ssh", ".gnupg", "Library/Keychains", "Library/Cookies",
			".aws", ".azure", ".config/gcloud", ".oci", ".kube", ".docker",
			".config/rclone", ".password-store", ".config/gh",
		} {
			list.Subtrees = append(list.Subtrees, filepath.Join(home, filepath.FromSlash(relative)))
		}
	}
	if strings.TrimSpace(configDir) != "" && filepath.IsAbs(configDir) {
		// OpenFox 本机配置目录（设备私钥、会话令牌、本地 Run 记录）。管理工作区在其下时由 GuardPolicy 单独放行。
		list.Subtrees = append(list.Subtrees, filepath.Clean(configDir))
	}
	return list
}

func (list ProtectedDirectories) normalize() (ProtectedDirectories, error) {
	subtrees, err := normalizeRoots(list.Subtrees)
	if err != nil {
		return ProtectedDirectories{}, err
	}
	exact, err := normalizeRoots(list.Exact)
	if err != nil {
		return ProtectedDirectories{}, err
	}
	return ProtectedDirectories{Subtrees: subtrees, Exact: exact, FoldCase: list.FoldCase}, nil
}

func (list ProtectedDirectories) fold(path string) string {
	if list.FoldCase {
		return strings.ToLower(path)
	}
	return path
}

// match 判断已解析的绝对路径是否命中名单，返回命中的受保护目录。调用方需先 normalize。
func (list ProtectedDirectories) match(resolved string) (string, bool) {
	candidate := list.fold(filepath.Clean(resolved))
	for _, root := range list.Exact {
		if list.fold(root) == candidate {
			return root, true
		}
	}
	for _, root := range list.Subtrees {
		folded := list.fold(root)
		if pathContains(folded, candidate) || pathContains(candidate, folded) {
			return root, true
		}
	}
	return "", false
}

// ProtectedMatch 解析 path（含符号链接）后判断是否命中名单；返回命中的受保护目录。
// 解析失败（例如权限不足）时按命中处理：拿不准的目录不交给 Run。
func (list ProtectedDirectories) ProtectedMatch(path string) (string, bool) {
	normalized, err := list.normalize()
	if err != nil {
		return "", true
	}
	resolved, err := canonicalPath(path)
	if err != nil {
		return "", true
	}
	return normalized.match(resolved)
}

// managedRootSafe 判断管理工作区根能否整体放行：它不能等于名单里的目录本身，也不能包含受保护子树。
// 位于受保护子树之内（例如默认的 <配置目录>/workspaces）是预期布局，可以放行。调用方需先 normalize。
func (list ProtectedDirectories) managedRootSafe(root string) bool {
	candidate := list.fold(filepath.Clean(root))
	for _, exact := range list.Exact {
		if list.fold(exact) == candidate {
			return false
		}
	}
	for _, subtree := range list.Subtrees {
		if pathContains(candidate, list.fold(subtree)) {
			return false
		}
	}
	return true
}
