import { useEffect, useMemo, useState } from "react";

import type {
  InfiniteData,
  UseInfiniteQueryResult,
} from "@tanstack/react-query";

import type {
  MarketPack,
  MarketPage,
  MarketSkill,
  MarketTemplate,
} from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Skeleton,
  Segmented,
  TextInput,
  cn,
} from "@openfox/ui";

import { MarketIcon } from "./MarketIcon";
import {
  MARKET_SORTS,
  PACKS_PER_PAGE,
  SEARCH_DEBOUNCE_MS,
  compactCount,
  pickText,
  type MarketSort,
} from "./model";
import {
  useMarketCatalog,
  useMarketSkills,
  useMarketTemplates,
} from "./queries";

// 市场列表：技能 / Agent 模板切换、搜索、精选包推荐、分类筛选、排序、卡片网格。
// 市场页和「从技能市场添加」的选择弹窗共用；skillsOnly 时只有技能。

type Kind = "skills" | "templates";

export function MarketBrowser({
  orgId,
  skillsOnly = false,
  onOpenSkill,
  onOpenTemplate,
}: {
  orgId: string;
  skillsOnly?: boolean;
  onOpenSkill: (id: string) => void;
  onOpenTemplate: (id: string) => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const catalog = useMarketCatalog(orgId);
  const [kind, setKind] = useState<Kind>("skills");
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  const [category, setCategory] = useState("");
  const [sort, setSort] = useState<MarketSort>("default");
  useEffect(() => {
    const timer = setTimeout(() => setQ(search.trim()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [search]);

  const filters = {
    ...(category ? { category } : {}),
    ...(q ? { q } : {}),
  };
  const skills = useMarketSkills(
    orgId,
    { ...filters, ...(sort !== "default" ? { sort } : {}) },
    kind === "skills",
  );
  const templates = useMarketTemplates(orgId, filters, kind === "templates");

  const categories = useMemo(
    () =>
      [...(catalog.data?.categories ?? [])]
        .filter(
          (c) => (kind === "skills" ? c.skill_count : c.template_count) > 0,
        )
        .sort((a, b) => a.order - b.order),
    [catalog.data, kind],
  );

  if (catalog.isError && !catalog.data)
    return (
      <div role="alert" className="flex flex-col items-start gap-2 p-6">
        <p className="text-body text-danger">{t("market.loadFailed")}</p>
        <Button
          variant="outline"
          size="28"
          onClick={() => void catalog.refetch()}
        >
          {t("market.retry")}
        </Button>
      </div>
    );
  if (!catalog.data)
    return (
      <div aria-busy="true" className="grid grid-cols-3 gap-3 p-6">
        {[0, 1, 2].map((index) => (
          <Skeleton key={index} className="h-32 rounded-xl" />
        ))}
      </div>
    );
  if (catalog.data.counts.skills === 0 && catalog.data.counts.templates === 0)
    return (
      <div className="flex flex-1 items-center justify-center p-6">
        <div className="flex w-[420px] max-w-full flex-col items-start gap-3">
          <Icon name="store" size={48} className="text-fg-muted" />
          <h2 className="text-body font-medium">{t("market.emptyTitle")}</h2>
          <p className="text-body-sm text-fg-muted">{t("market.emptyDesc")}</p>
        </div>
      </div>
    );

  const list = (
    kind === "skills" ? skills : templates
  ) as UseInfiniteQueryResult<
    InfiniteData<MarketPage<MarketSkill | MarketTemplate>>
  >;
  const items = (list.data?.pages ?? []).flatMap((page) => page.items);
  const total = list.data?.pages[0]?.total ?? 0;
  const showPacks = kind === "skills" && (catalog.data.packs ?? []).length > 0;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        {!skillsOnly && (
          <Segmented
            label={t("market.kind.label")}
            value={kind}
            onChange={(value) => {
              setKind(value);
              setCategory("");
            }}
            options={[
              {
                value: "skills" as Kind,
                label: t("market.kind.skills"),
                icon: <Icon name="sparkles" />,
              },
              {
                value: "templates" as Kind,
                label: t("market.kind.templates"),
                icon: <Icon name="bot" />,
              },
            ]}
          />
        )}
        <TextInput
          variant="field"
          aria-label={t("market.search.label")}
          placeholder={
            kind === "skills"
              ? t("market.search.skills")
              : t("market.search.templates")
          }
          leading={<Icon name="search" />}
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          className="ml-auto !w-[290px] shrink-0"
        />
      </div>

      {showPacks && (
        <PackRail packs={catalog.data.packs} onOpenSkill={onOpenSkill} />
      )}

      <div className="flex items-center gap-2">
        <div
          role="group"
          aria-label={t("market.filter.label")}
          className="flex min-w-0 flex-1 flex-wrap gap-1.5"
        >
          <Chip active={category === ""} onClick={() => setCategory("")}>
            {t("market.filter.all")}
          </Chip>
          {categories.map((c) => (
            <Chip
              key={c.id}
              active={category === c.id}
              onClick={() => setCategory(c.id)}
            >
              {pickText(c.name, locale)}
            </Chip>
          ))}
        </div>
        {kind === "skills" && (
          <Menu
            placement="bottom-end"
            label={t("market.sort.label")}
            trigger={(props) => (
              <Button
                ref={props.ref}
                variant="ghost"
                size="28"
                aria-label={`${t("market.sort.label")}：${t(`market.sort.${sort}`)}`}
                aria-haspopup="menu"
                aria-expanded={props.open}
                trailing={<Icon name="chevron-down" />}
                onClick={props.toggle}
              >
                {t(`market.sort.${sort}`)}
              </Button>
            )}
          >
            {MARKET_SORTS.map((value) => (
              <MenuItem
                key={value}
                selected={sort === value}
                onSelect={() => setSort(value)}
              >
                {t(`market.sort.${value}`)}
              </MenuItem>
            ))}
          </Menu>
        )}
      </div>

      {list.isError && !list.data ? (
        <div role="alert" className="flex flex-col items-start gap-2">
          <p className="text-body text-danger">{t("market.loadFailed")}</p>
          <Button
            variant="outline"
            size="28"
            onClick={() => void list.refetch()}
          >
            {t("market.retry")}
          </Button>
        </div>
      ) : !list.data ? (
        <div aria-busy="true" className="grid grid-cols-3 gap-3">
          {[0, 1, 2].map((index) => (
            <Skeleton key={index} className="h-32 rounded-xl" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <p className="py-10 text-center text-body-sm text-fg-muted">
          {q ? t("market.noResults", { q }) : t("market.noItems")}
        </p>
      ) : (
        <>
          {q && (
            <p className="text-body-sm text-fg-muted">
              {t("market.resultCount", { count: total })}
            </p>
          )}
          <div className="@container">
            <div className="grid grid-cols-1 gap-3 @[640px]:grid-cols-2 @[960px]:grid-cols-3">
              {kind === "skills"
                ? (items as MarketSkill[]).map((skill) => (
                    <SkillCard
                      key={skill.market_skill_id}
                      skill={skill}
                      onOpen={() => onOpenSkill(skill.market_skill_id)}
                    />
                  ))
                : (items as MarketTemplate[]).map((template) => (
                    <TemplateCard
                      key={template.market_template_id}
                      template={template}
                      onOpen={() => onOpenTemplate(template.market_template_id)}
                    />
                  ))}
            </div>
          </div>
          {list.hasNextPage && (
            <Button
              variant="outline"
              className="self-center"
              loading={list.isFetchingNextPage}
              onClick={() => void list.fetchNextPage()}
            >
              {t("market.loadMore")}
            </Button>
          )}
        </>
      )}
    </div>
  );
}

function Chip({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "h-7 rounded-full border px-3 text-body-sm whitespace-nowrap",
        active
          ? "border-primary bg-primary text-fg-inverse"
          : "border-line-default text-fg-secondary hover:bg-surface-hover",
      )}
    >
      {children}
    </button>
  );
}

/** 精选包的浅色底：主题色 10% 透明度；不是 #rrggbb 时不加底色。 */
function packBackground(tint: string): string | undefined {
  return /^#[0-9a-fA-F]{6}$/.test(tint) ? `${tint}1a` : undefined;
}

/** 精选包推荐区：横向排列，左右箭头翻页。 */
function PackRail({
  packs,
  onOpenSkill,
}: {
  packs: MarketPack[];
  onOpenSkill: (id: string) => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const [page, setPage] = useState(0);
  const pages = Math.max(1, Math.ceil(packs.length / PACKS_PER_PAGE));
  const visible = packs.slice(
    page * PACKS_PER_PAGE,
    (page + 1) * PACKS_PER_PAGE,
  );
  return (
    <section
      aria-label={t("market.packs.title")}
      className="flex flex-col gap-2"
    >
      <div className="flex items-center gap-2">
        <h2 className="flex-1 text-body font-medium">
          {t("market.packs.title")}
        </h2>
        <IconButton
          label={t("market.packs.prev")}
          icon={<Icon name="chevron-left" />}
          size="28"
          disabled={page === 0}
          onClick={() => setPage(page - 1)}
        />
        <IconButton
          label={t("market.packs.next")}
          icon={<Icon name="chevron-right" />}
          size="28"
          disabled={page >= pages - 1}
          onClick={() => setPage(page + 1)}
        />
      </div>
      <div className="grid grid-cols-3 gap-3">
        {visible.map((pack) => (
          <article
            key={pack.id}
            aria-label={pickText(pack.name, locale)}
            style={{ backgroundColor: packBackground(pack.tint) }}
            className="flex flex-col gap-2 rounded-xl border border-line-default p-3.5 transition-colors hover:brightness-95"
          >
            <div className="flex items-center gap-2">
              <span
                aria-hidden
                style={{ backgroundColor: pack.tint }}
                className="size-2.5 shrink-0 rounded-full"
              />
              <h3 className="min-w-0 flex-1 truncate text-body font-medium">
                {pickText(pack.name, locale)}
              </h3>
              <span aria-hidden className="flex shrink-0 items-center">
                {pack.members.slice(0, 4).map((member) => (
                  <span
                    key={member.market_skill_id}
                    className={cn(
                      "-ml-1.5 rounded-lg ring-2 ring-surface-card first:ml-0",
                      !member.available && "opacity-50",
                    )}
                  >
                    <MarketIcon
                      src={member.icon}
                      name={pickText(member.name, locale)}
                      size={24}
                    />
                  </span>
                ))}
              </span>
            </div>
            <span className="text-caption text-fg-muted">
              {t("market.packs.count", { count: pack.members.length })}
            </span>
            <ul className="flex flex-col gap-1">
              {pack.members.slice(0, 4).map((member) => {
                const label =
                  pickText(member.name, locale) || member.market_skill_id;
                return (
                  <li key={member.market_skill_id}>
                    <button
                      type="button"
                      disabled={!member.available}
                      title={
                        member.available
                          ? undefined
                          : t("market.packs.unavailable")
                      }
                      onClick={() => onOpenSkill(member.market_skill_id)}
                      className="flex w-full items-center gap-2 rounded-md px-1 py-0.5 text-left text-body-sm hover:bg-surface-active disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent"
                    >
                      <span className="truncate">{label}</span>
                    </button>
                  </li>
                );
              })}
            </ul>
          </article>
        ))}
      </div>
    </section>
  );
}

function SkillCard({
  skill,
  onOpen,
}: {
  skill: MarketSkill;
  onOpen: () => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const name = pickText(skill.name, locale) || skill.market_skill_id;
  return (
    <button
      type="button"
      aria-label={t("market.card.openSkill", { name })}
      onClick={onOpen}
      className="flex flex-col gap-2.5 rounded-xl border border-line-default bg-surface-card p-3.5 text-left hover:bg-surface-hover"
    >
      <span className="flex items-center gap-2.5">
        <MarketIcon src={skill.icon} name={name} size={36} />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-body font-medium">{name}</span>
          <span className="truncate text-caption text-fg-muted">
            {skill.publisher.name}
          </span>
        </span>
      </span>
      <span className="line-clamp-2 min-h-10 text-body-sm text-fg-secondary">
        {pickText(skill.description, locale)}
      </span>
      <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-caption text-fg-muted">
        {skill.tags.slice(0, 2).map((tag, index) => (
          <span key={index} className="rounded bg-surface-muted px-1.5 py-0.5">
            {pickText(tag, locale)}
          </span>
        ))}
        <span className="ml-auto flex items-center gap-1">
          <Icon name="download" />
          {compactCount(skill.downloads)}
        </span>
        <span className="flex items-center gap-1">
          <Icon name="star" />
          {compactCount(skill.stars)}
        </span>
      </span>
    </button>
  );
}

function TemplateCard({
  template,
  onOpen,
}: {
  template: MarketTemplate;
  onOpen: () => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const name = pickText(template.name, locale) || template.market_template_id;
  return (
    <button
      type="button"
      aria-label={t("market.card.openTemplate", { name })}
      onClick={onOpen}
      className="flex flex-col gap-2.5 rounded-xl border border-line-default bg-surface-card p-3.5 text-left hover:bg-surface-hover"
    >
      <span className="flex items-center gap-2.5">
        <MarketIcon src={template.avatar?.url} name={name} size={36} round />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-body font-medium">{name}</span>
          <span className="truncate text-caption text-fg-muted">
            {pickText(template.job_title, locale)}
          </span>
        </span>
      </span>
      <span className="line-clamp-2 min-h-10 text-body-sm text-fg-secondary">
        {pickText(template.tagline, locale) ||
          pickText(template.description, locale)}
      </span>
      <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-caption text-fg-muted">
        {template.keywords.slice(0, 2).map((word) => (
          <span key={word} className="rounded bg-surface-muted px-1.5 py-0.5">
            {word}
          </span>
        ))}
        <span className="ml-auto">
          {t("market.card.skillCount", { count: template.skill_count })}
        </span>
      </span>
    </button>
  );
}
