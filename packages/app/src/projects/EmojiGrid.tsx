import { useT } from "@openfox/i18n";
import { cn } from "@openfox/ui";

const EMOJIS = [
  "🚀",
  "🎯",
  "💡",
  "🔥",
  "⭐",
  "📦",
  "🛠️",
  "🧪",
  "📈",
  "📣",
  "🎨",
  "🔒",
  "🌐",
  "📱",
  "💻",
  "🧩",
  "📚",
  "🐛",
  "⚡",
  "🏁",
  "🎉",
  "❤️",
  "🌱",
  "🧭",
];

/** 表情选择网格（专项图标与视图图标共用）；再点一次已选表情即清除。 */
export function EmojiGrid({
  value,
  onChange,
}: {
  value: string | null;
  onChange: (emoji: string | null) => void;
}) {
  const t = useT();
  return (
    <div
      role="group"
      aria-label={t("projects.emoji")}
      className="grid grid-cols-8 gap-1"
    >
      {EMOJIS.map((emoji) => (
        <button
          key={emoji}
          type="button"
          aria-label={emoji}
          aria-pressed={emoji === value}
          onClick={() => onChange(emoji === value ? null : emoji)}
          className={cn(
            "flex size-8 items-center justify-center rounded-lg text-title hover:bg-surface-hover",
            emoji === value && "bg-surface-active",
          )}
        >
          {emoji}
        </button>
      ))}
    </div>
  );
}
