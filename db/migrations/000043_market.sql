-- 市场：平台自建的技能与 Agent 模板目录，以及空间技能与市场版本的关联。
--
-- 仅扩展（expand-only）：
--   - 市场目录是全平台共用的只读数据，不属于任何组织，所以这几张表不开 RLS：
--     应用角色 openfox_app 只有 SELECT；写入只由导入工具（openfox-server market import，以 openfox_migrator 身份）完成。
--   - market_catalog：单行（id = 1），目录版本、摘要、来源、分类与精选包（jsonb）。
--   - market_skills：市场技能（skill_id 为来源方 ID，如 skillhub.ake.dev-god），一个技能只保留当前版本；
--     多语言字段（name / description / tags）为 {"zh-Hans": …, "en": …} 结构；图标为 https 地址或空。
--     file_count / total_bytes 与文件清单一致（≤ 100 个、≤ 5 MB，与 5c 空间技能相同上限）。
--   - market_skill_files：市场技能的文件清单，内容放在 OPENFOX_STORAGE_ROOT（file_key），路径规则与 skill_files 相同。
--   - market_templates：Agent 模板。instructions 为 {"instructions.zh-Hans.md": …} 结构（≤ 256 KB），
--     skill_refs 为 [{skillId, version}]，引用的技能可以不在本平台目录里（界面显示「市场中暂无」）。
--   - skills 增加 market_skill_id / market_version：从市场添加的空间技能记住来源版本，用于判断「可更新」。
-- 锁：skills 加两个可空列（只改目录，不重写表）和一个 CHECK（需扫描表，行数受每空间 500 个限制）。
-- 回滚：应用回退后新表与新列闲置即可；确需移除时见 db/migrations/README.md 的 000043 条目。

SET ROLE openfox_migrator;

CREATE TABLE openfox.market_catalog (
    id smallint PRIMARY KEY DEFAULT 1,
    source text NOT NULL,
    catalog_version text NOT NULL,
    digest text NOT NULL,
    categories jsonb NOT NULL DEFAULT '[]',
    packs jsonb NOT NULL DEFAULT '[]',
    imported_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT market_catalog_singleton CHECK (id = 1),
    CONSTRAINT market_catalog_source_valid CHECK (source ~ '^[a-z][a-z0-9-]{0,31}$'),
    CONSTRAINT market_catalog_version_valid CHECK (char_length(catalog_version) BETWEEN 1 AND 64 AND char_length(digest) <= 128),
    CONSTRAINT market_catalog_shape CHECK (
        jsonb_typeof(categories) = 'array' AND jsonb_typeof(packs) = 'array'
        AND pg_column_size(categories) <= 262144 AND pg_column_size(packs) <= 262144
    )
);

CREATE TABLE openfox.market_skills (
    skill_id text PRIMARY KEY,
    version text NOT NULL,
    digest text NOT NULL DEFAULT '',
    name jsonb NOT NULL,
    description jsonb NOT NULL DEFAULT '{}',
    icon text,
    category_ids text[] NOT NULL DEFAULT '{}',
    tags jsonb NOT NULL DEFAULT '[]',
    stats jsonb NOT NULL DEFAULT '{}',
    publisher jsonb NOT NULL DEFAULT '{}',
    origin text NOT NULL DEFAULT '',
    featured boolean NOT NULL DEFAULT false,
    downloads bigint NOT NULL DEFAULT 0,
    file_count integer NOT NULL DEFAULT 0,
    total_bytes bigint NOT NULL DEFAULT 0,
    search_text text NOT NULL DEFAULT '',
    imported_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT market_skills_id_valid CHECK (skill_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$'),
    CONSTRAINT market_skills_version_valid CHECK (char_length(version) BETWEEN 1 AND 64 AND char_length(digest) <= 128),
    CONSTRAINT market_skills_icon_valid CHECK (icon IS NULL OR (icon ~ '^https://' AND char_length(icon) <= 1024)),
    CONSTRAINT market_skills_shape CHECK (
        jsonb_typeof(name) = 'object' AND jsonb_typeof(description) = 'object' AND jsonb_typeof(tags) = 'array'
        AND jsonb_typeof(stats) = 'object' AND jsonb_typeof(publisher) = 'object'
        AND pg_column_size(description) <= 32768 AND pg_column_size(tags) <= 8192
        AND cardinality(category_ids) <= 20 AND char_length(origin) <= 32 AND char_length(search_text) <= 16384
    ),
    CONSTRAINT market_skills_size_valid CHECK (file_count BETWEEN 0 AND 100 AND total_bytes BETWEEN 0 AND 5242880 AND downloads >= 0)
);
CREATE INDEX market_skills_downloads_idx ON openfox.market_skills (downloads DESC, skill_id);
CREATE INDEX market_skills_category_idx ON openfox.market_skills USING gin (category_ids);

CREATE TABLE openfox.market_skill_files (
    skill_id text NOT NULL REFERENCES openfox.market_skills (skill_id) ON DELETE CASCADE,
    path text NOT NULL,
    file_key text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    mime text NOT NULL DEFAULT '',
    PRIMARY KEY (skill_id, path),
    CONSTRAINT market_skill_files_path_valid CHECK (
        char_length(path) BETWEEN 1 AND 256
        AND path !~ '^/'
        AND path !~ '[\\[:cntrl:]]'
        AND path !~ '(^|/)\.{0,2}(/|$)'
    ),
    CONSTRAINT market_skill_files_file_valid CHECK (
        length(file_key) BETWEEN 1 AND 256 AND size_bytes BETWEEN 0 AND 5242880
        AND sha256 ~ '^[0-9a-f]{64}$' AND char_length(mime) <= 128
    )
);

CREATE TABLE openfox.market_templates (
    template_id text PRIMARY KEY,
    version text NOT NULL,
    name jsonb NOT NULL,
    job_title jsonb NOT NULL DEFAULT '{}',
    tagline jsonb NOT NULL DEFAULT '{}',
    description jsonb NOT NULL DEFAULT '{}',
    category_ids text[] NOT NULL DEFAULT '{}',
    keywords text[] NOT NULL DEFAULT '{}',
    avatar jsonb NOT NULL DEFAULT '{}',
    quick_prompts jsonb NOT NULL DEFAULT '[]',
    instructions jsonb NOT NULL DEFAULT '{}',
    skill_refs jsonb NOT NULL DEFAULT '[]',
    origin text NOT NULL DEFAULT '',
    position integer NOT NULL DEFAULT 0,
    published_at timestamptz,
    search_text text NOT NULL DEFAULT '',
    imported_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT market_templates_id_valid CHECK (template_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$'),
    CONSTRAINT market_templates_version_valid CHECK (char_length(version) BETWEEN 1 AND 64),
    CONSTRAINT market_templates_shape CHECK (
        jsonb_typeof(name) = 'object' AND jsonb_typeof(job_title) = 'object' AND jsonb_typeof(tagline) = 'object'
        AND jsonb_typeof(description) = 'object' AND jsonb_typeof(avatar) = 'object'
        AND jsonb_typeof(quick_prompts) = 'array' AND jsonb_typeof(instructions) = 'object' AND jsonb_typeof(skill_refs) = 'array'
        AND pg_column_size(instructions) <= 262144 AND pg_column_size(description) <= 32768
        AND pg_column_size(quick_prompts) <= 32768 AND jsonb_array_length(skill_refs) <= 50
        AND cardinality(category_ids) <= 20 AND cardinality(keywords) <= 50
        AND char_length(origin) <= 32 AND char_length(search_text) <= 16384
    ),
    CONSTRAINT market_templates_position_valid CHECK (position >= 0)
);
CREATE INDEX market_templates_order_idx ON openfox.market_templates (position, template_id);
CREATE INDEX market_templates_category_idx ON openfox.market_templates USING gin (category_ids);

-- 从市场添加的空间技能：记住来源与版本（两列同时为空或同时有值，且只用于 source_kind = 'market'）。
ALTER TABLE openfox.skills ADD COLUMN market_skill_id text;
ALTER TABLE openfox.skills ADD COLUMN market_version text;
ALTER TABLE openfox.skills ADD CONSTRAINT skills_market_valid CHECK (
    (market_skill_id IS NULL AND market_version IS NULL)
    OR (source_kind = 'market' AND market_skill_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$'
        AND char_length(market_version) BETWEEN 1 AND 64)
);
CREATE INDEX skills_market_idx ON openfox.skills (organization_id, space_id, market_skill_id) WHERE market_skill_id IS NOT NULL;

RESET ROLE;

GRANT SELECT ON openfox.market_catalog, openfox.market_skills, openfox.market_skill_files, openfox.market_templates TO openfox_app;
