import EmojiPicker, {
  Categories,
  EmojiStyle,
  SkinTonePickerLocation,
  type EmojiDataLoader,
  type PickerLabels,
  type SkinTonesValue,
} from "emoji-picker-react";
import { useState, type CSSProperties } from "react";

import { useI18n, useT } from "@openfox/i18n";

// 表情选择器（emoji-picker-react 5.1.4），由 Reactions 用 React.lazy 懒加载，不进首屏包。
// 原生表情字体（emojiStyle=native），数据随包内置，不访问 CDN。

/** 中文数据集单独分包，只在中文界面第一次打开时加载；英文用库自带的默认数据。 */
const loadZh: EmojiDataLoader = () =>
  import("emoji-picker-react/data/emojis-zh");

/** 首次打开、还没有「最近使用」时的推荐区：沿用原来的 12 个常用表情。 */
export const SUGGESTED_EMOJIS = [
  "👍",
  "❤️",
  "🎉",
  "😄",
  "👀",
  "🚀",
  "🙏",
  "✅",
  "🤔",
  "🔥",
  "👏",
  "😢",
];

const RECENT_KEY = "openfox:emoji:recent";
const SKIN_KEY = "openfox:emoji:skin";
const RECENT_MAX = 24;
const SKIN_TONES = ["neutral", "1f3fb", "1f3fc", "1f3fd", "1f3fe", "1f3ff"];

// 浏览器本地存储可能不可用（隐私模式、被禁用），读写都兜底
function readStorage(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}
function writeStorage(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // 忽略：只是失去「最近使用」或默认肤色记忆
  }
}

function readRecent(): string[] {
  try {
    const value: unknown = JSON.parse(readStorage(RECENT_KEY) ?? "[]");
    return Array.isArray(value)
      ? value.filter((x): x is string => typeof x === "string")
      : [];
  } catch {
    return [];
  }
}

function readSkin(): SkinTonesValue {
  const value = readStorage(SKIN_KEY);
  return (
    value && SKIN_TONES.includes(value) ? value : "neutral"
  ) as SkinTonesValue;
}

/** 用现有主题变量映射选择器配色；深浅色随 :root[data-theme] 自动切换。 */
const THEME_VARS = {
  "--epr-bg-color": "var(--color-surface-card)",
  "--epr-category-label-bg-color": "var(--color-surface-card)",
  "--epr-category-label-text-color": "var(--color-fg-muted)",
  "--epr-text-color": "var(--color-fg-default)",
  "--epr-picker-border-color": "transparent",
  "--epr-hover-bg-color": "var(--color-surface-hover)",
  "--epr-hover-bg-color-reduced-opacity": "var(--color-surface-hover)",
  "--epr-focus-bg-color": "var(--color-surface-active)",
  "--epr-highlight-color": "var(--color-line-focus)",
  "--epr-search-input-bg-color": "var(--color-surface-muted)",
  "--epr-search-input-bg-color-active": "var(--color-surface-card)",
  "--epr-search-input-text-color": "var(--color-fg-default)",
  "--epr-search-input-placeholder-color": "var(--color-fg-subtle)",
  "--epr-search-border-color": "var(--color-line-default)",
  "--epr-search-border-color-active": "var(--color-line-focus)",
  "--epr-category-icon-active-color": "var(--color-fg-default)",
  "--epr-category-icon-inactive-color": "var(--color-fg-subtle)",
  "--epr-skin-tone-picker-menu-color": "var(--color-surface-card)",
  "--epr-emoji-variation-picker-bg-color": "var(--color-surface-card)",
  "--epr-skin-tone-outer-border-color": "var(--color-line-default)",
  "--epr-skin-tone-inner-border-color": "var(--color-surface-card)",
  "--epr-font-family": "inherit",
} as CSSProperties;

export const PICKER_WIDTH = 336;
export const PICKER_HEIGHT = 400;

const LABEL_KEYS = [
  "searchPlaceholder",
  "searchLabel",
  "searchClear",
  "searchResultsNone",
  "searchResultsOne",
  "searchResultsMany",
  "categoryNavigation",
  "loading",
  "loadingError",
  "retryLoading",
  "skinToneNeutral",
  "skinToneLight",
  "skinToneMediumLight",
  "skinToneMedium",
  "skinToneMediumDark",
  "skinToneDark",
] as const satisfies readonly (keyof PickerLabels)[];

const CATEGORY_KEYS = [
  [Categories.SUGGESTED, "suggested"],
  [Categories.SMILEYS_PEOPLE, "smileysPeople"],
  [Categories.ANIMALS_NATURE, "animalsNature"],
  [Categories.FOOD_DRINK, "foodDrink"],
  [Categories.ACTIVITIES, "activities"],
  [Categories.TRAVEL_PLACES, "travelPlaces"],
  [Categories.OBJECTS, "objects"],
  [Categories.SYMBOLS, "symbols"],
  [Categories.FLAGS, "flags"],
] as const;

/**
 * 选中后回调完整的表情字符串（含肤色、U+FE0F 等），由调用方走现有的添加反应接口。
 * 「最近使用」与默认肤色只存在浏览器本地（openfox:emoji:*），不加后端字段。
 */
export default function EmojiPickerPanel({
  onPick,
}: {
  onPick: (emoji: string) => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const [recent, setRecent] = useState(readRecent);
  const [skinTone, setSkinTone] = useState(readSkin);
  const labels: Partial<PickerLabels> = Object.fromEntries(
    LABEL_KEYS.map((key) => [key, t(`tasks.detail.emojiPicker.${key}`)]),
  );
  const categories = CATEGORY_KEYS.map(([category, key]) => ({
    category,
    name: t(`tasks.detail.emojiPicker.categories.${key}`),
  }));
  return (
    <EmojiPicker
      {...(locale === "zh-CN" ? { emojiData: loadZh } : {})}
      emojiStyle={EmojiStyle.NATIVE}
      colorScheme="light"
      width={PICKER_WIDTH}
      height={PICKER_HEIGHT}
      style={THEME_VARS}
      autoFocusSearch
      previewConfig={{ showPreview: false }}
      skinTonePickerLocation={SkinTonePickerLocation.SEARCH}
      labels={labels}
      categories={categories}
      // 自己管理「最近使用」：没有记录时用原来的 12 个
      suggestedEmojis={recent.length ? recent : SUGGESTED_EMOJIS}
      skinTone={skinTone}
      onSkinToneChange={(tone) => {
        setSkinTone(tone);
        writeStorage(SKIN_KEY, tone);
      }}
      onEmojiClick={(data) => {
        const id = data.unifiedWithoutSkinTone;
        const next = [id, ...recent.filter((u) => u !== id)].slice(
          0,
          RECENT_MAX,
        );
        setRecent(next);
        writeStorage(RECENT_KEY, JSON.stringify(next));
        onPick(data.emoji);
      }}
    />
  );
}
