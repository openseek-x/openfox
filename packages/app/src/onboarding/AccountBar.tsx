import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";

import { useT } from "@openfox/i18n";
import { Avatar, Button } from "@openfox/ui";

import { useApi } from "../context";
import { clearNavMemory } from "../shell/navMemory";

/** 首登页右上角：当前账号 + 退出登录。 */
export function AccountBar({ email }: { email: string }) {
  const t = useT();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  if (!email) return null;
  return (
    <span className="flex items-center gap-2 text-body-sm text-fg-secondary">
      <Avatar name={email} size={24} />
      {email}
      <Button
        variant="ghost"
        size="28"
        onClick={async () => {
          await api.auth.logout().catch(() => undefined);
          queryClient.clear();
          clearNavMemory();
          navigate("/login", { replace: true });
        }}
      >
        {t("common.logout")}
      </Button>
    </span>
  );
}
