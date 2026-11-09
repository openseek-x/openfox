// Keep the reviewed display order. Only first-party published remote URLs are prefilled.
export interface ToolCatalogEntry {
  id: string;
  name: string;
  description: string;
  category: string;
  kind: "MCP" | "CLI";
  endpoint?: string;
  endpointExample?: string;
  authHeader?: string;
  initialHeaders?: Record<string, string>;
  website?: string;
  iconUrl?: string;
  useCases?: string[];
  prompts?: string[];
  authMode?: "none" | "api_key" | "oauth";
}
export const toolCatalog: ToolCatalogEntry[] = [
  {
    id: "feishu",
    name: "飞书",
    category: "办公协作",
    kind: "CLI",
    description: "通过飞书官方命令行工具处理消息、日历、文档、表格和知识库。",
  },
  {
    id: "kdocs-mcp",
    useCases: ["检索云端文件", "读取文档内容", "更新表格"],
    prompts: [
      "列出包含「项目计划」的云文档",
      "找出排期表中本周到期的工作",
      "概括这份周报中的未完成事项",
    ],
    name: "金山文档",
    category: "办公协作",
    kind: "MCP",
    description: "通过金山文档个人令牌读取或编辑云端文档、表格和目录。",
    endpoint: "https://mcp-center.wps.cn/skill_hub/mcp",
    authMode: "api_key",
    website: "https://www.kdocs.cn/latest",
    initialHeaders: {
      "X-Skill-Version": "1.3.1",
    },
  },
  {
    id: "tencent-docs",
    name: "腾讯文档",
    category: "办公协作",
    kind: "MCP",
    description:
      "从腾讯文档授权页复制个人 Token，在高级设置的 Authorization 请求头中原样填写。",
    endpoint: "https://docs.qq.com/openapi/mcp",
    authMode: "none",
    website: "https://developer.cloud.tencent.com/mcp/server/11803",
    useCases: ["读取在线文档", "检索智能表格"],
    prompts: ["列出当前目录下的项目文档", "找出表格中尚未确认的记录"],
    initialHeaders: {
      Authorization: "",
    },
  },
  {
    id: "netease-mail",
    name: "网易邮箱",
    category: "办公协作",
    kind: "MCP",
    description:
      "通过 IMAP/SMTP 连接邮箱，支持收发邮件、搜索、附件下载。支持 163、126、yeah.net 等网易邮箱及其他标准 IMAP/SMTP 邮箱。",
  },
  {
    id: "airtable",
    name: "Airtable",
    category: "办公协作",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 Airtable MCP,查询 base、读取记录并创建或更新行。",
    endpoint: "https://mcp.airtable.com/mcp",
    website:
      "https://support.airtable.com/articles/9897799762-Using-the-Airtable-MCP-server",
    authMode: "oauth",
  },
  {
    id: "asana",
    name: "Asana",
    category: "办公协作",
    kind: "MCP",
    description:
      "将你的智能体连接到 Asana MCP,搜索、创建和更新任务、项目与状态更新。",
    endpoint: "https://mcp.asana.com/v2/mcp",
    website: "https://developers.asana.com/docs/using-asanas-mcp-server",
    authMode: "oauth",
  },
  {
    id: "miro",
    name: "Miro",
    category: "办公协作",
    kind: "MCP",
    description: "授权时选择白板所在的 Miro 团队。",
    endpoint: "https://mcp.miro.com/",
    authMode: "oauth",
    website: "https://developers.miro.com/docs/connecting-to-miro-mcp",
    useCases: ["读取团队白板", "整理白板内容"],
    prompts: ["总结这块白板里的讨论事项"],
  },
  {
    id: "notion",
    authMode: "oauth",
    useCases: ["检索工作区知识库", "更新页面和数据库"],
    prompts: ["查找最近更新的项目说明页"],
    name: "Notion",
    category: "办公协作",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 Notion MCP,搜索、读取并更新工作区中的页面、数据库与评论。",
    endpoint: "https://mcp.notion.com/mcp",
    website:
      "https://registry.modelcontextprotocol.io/v0.1/servers/com.notion%2Fmcp/versions/latest",
  },
  {
    id: "tencent-cloudbase",
    name: "腾讯云 CloudBase",
    category: "开发工具",
    kind: "MCP",
    description:
      "连接国内站，在腾讯云授权页选择环境。国际站使用 tcb-api.tencentcloud.com。",
    endpoint: "https://tcb-api.cloud.tencent.com/mcp/v1",
    authMode: "oauth",
    website:
      "https://docs.cloudbase.net/ai/cloudbase-ai-toolkit/connection-modes",
    useCases: ["浏览环境配置", "查询云函数"],
    prompts: ["列出当前环境的云函数"],
  },
  {
    id: "apifox",
    name: "Apifox",
    category: "开发工具",
    kind: "CLI",
    description:
      "通过自然语言管理 Apifox 项目：接口、数据模型、测试用例、测试场景、环境与分支等操作。",
  },
  {
    id: "atlassian",
    name: "Atlassian",
    category: "开发工具",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 Atlassian MCP,处理 Jira 工作项、Confluence 页面与 Compass 组件。",
    endpoint: "https://mcp.atlassian.com/v2/mcp",
    website:
      "https://support.atlassian.com/atlassian-rovo-mcp-server/docs/getting-started-with-the-atlassian-remote-mcp-server/",
    authMode: "oauth",
  },
  {
    id: "bugly-token",
    name: "Bugly 质量概览",
    category: "开发工具",
    kind: "MCP",
    description: "查看产品的质量概览 包括崩溃率 anr率 foom（oom）率 启动耗时",
  },
  {
    id: "cloudflare",
    name: "Cloudflare",
    category: "开发工具",
    kind: "MCP",
    description: "连接 Cloudflare API，按账号授权访问 DNS、Workers 等服务。",
    endpoint: "https://mcp.cloudflare.com/mcp",
    authMode: "oauth",
    website:
      "https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/",
    useCases: ["检查 DNS 配置", "查阅 Workers 服务"],
    prompts: ["列出这个域名的 DNS 记录"],
  },
  {
    id: "cnb-api",
    name: "CNB",
    category: "开发工具",
    kind: "CLI",
    description:
      "通过自然语言管理 CNB 平台：仓库、Issue、Pull Request、流水线、制品库等操作。",
  },
  {
    id: "gitee-mcp",
    name: "Gitee",
    category: "开发工具",
    kind: "MCP",
    description: "使用 Gitee 个人访问令牌连接仓库、Issue 和 Pull Request。",
    endpoint: "https://api.gitee.com/mcp",
    authMode: "api_key",
    website:
      "https://gitee.com/oschina/mcp-gitee/blob/master/docs/install/codex.md",
    useCases: ["查看仓库", "检索 Issue"],
    prompts: ["列出这个仓库尚未关闭的 Issue"],
  },
  {
    id: "github",
    name: "GitHub",
    category: "开发工具",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 GitHub MCP,搜索代码、管理 GitHub Issue 与 PR,并查看 CI 运行状态。",
    endpoint: "https://api.githubcopilot.com/mcp/",
    website: "https://github.com/github/github-mcp-server",
    authMode: "api_key",
  },
  {
    id: "honeycomb",
    name: "Honeycomb",
    category: "开发工具",
    kind: "MCP",
    description:
      "将 Agent 连接到 Honeycomb MCP,查询链路与事件、查看 SLO 并排查线上故障。",
    endpoint: "https://mcp.honeycomb.io/mcp",
    website: "https://docs.honeycomb.io/integrations/mcp/concepts",
    authMode: "oauth",
  },
  {
    id: "huggingface",
    authMode: "none",
    useCases: ["搜索模型与数据集", "查看模型卡和论文"],
    prompts: ["查找支持中文的语音识别模型"],
    name: "Hugging Face",
    category: "开发工具",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 Hugging Face MCP,搜索模型、数据集与 Spaces 并阅读其说明。无需登录。",
    endpoint: "https://huggingface.co/mcp",
    website:
      "https://registry.modelcontextprotocol.io/v0.1/servers/co.huggingface%2Fhf-mcp-server/versions/latest",
  },
  {
    id: "linear",
    name: "Linear",
    category: "开发工具",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 Linear MCP,查找、创建和更新 Linear 中的工作项、项目与评论。",
    endpoint: "https://mcp.linear.app/mcp",
    website: "https://linear.app/docs/mcp",
    authMode: "oauth",
  },
  {
    id: "neon-mcp",
    name: "Neon",
    category: "开发工具",
    kind: "MCP",
    description: "通过 Neon 官方远程 MCP 查询和管理 Postgres 数据库。",
    endpoint: "https://mcp.neon.tech/mcp",
    website: "https://neon.com/docs/ai/neon-mcp-server",
    authMode: "oauth",
  },
  {
    id: "netlify",
    name: "Netlify",
    category: "开发工具",
    kind: "MCP",
    description: "连接 Netlify 官方远程 MCP，通过 Netlify 授权访问站点。",
    endpoint: "https://mcp.netlify.com/mcp",
    authMode: "oauth",
    website:
      "https://docs.netlify.com/build/build-with-ai/agent-setup-guides/agent-setup-overview/",
    useCases: ["检查站点部署"],
    prompts: ["查看这个站点最近一次部署"],
  },
  {
    id: "sentry",
    name: "Sentry",
    category: "开发工具",
    kind: "MCP",
    description:
      "检索错误与性能问题。可将组织或项目的 slug 附在地址后以缩小范围。",
    endpoint: "https://mcp.sentry.dev/mcp",
    authMode: "oauth",
    website: "https://mcp.sentry.dev/",
    useCases: ["查找错误", "查看性能问题"],
    prompts: ["找出这个项目最近出现次数最多的错误"],
  },
  {
    id: "supabase",
    name: "Supabase",
    category: "开发工具",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 Supabase MCP,查看表结构、执行查询并管理项目。",
    endpoint: "https://mcp.supabase.com/mcp",
    website: "https://supabase.com/mcp",
    authMode: "oauth",
  },
  {
    id: "tapd",
    name: "TAPD",
    category: "开发工具",
    kind: "MCP",
    description:
      "管理需求、缺陷、任务和迭代。查询项目进度、拆分需求、流转状态、填写工时，覆盖需求到发布的研发全生命周期。",
  },
  {
    id: "vercel",
    name: "Vercel",
    category: "开发工具",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 Vercel MCP,查看项目、部署记录与构建日志。",
    endpoint: "https://mcp.vercel.com",
    website: "https://vercel.com/docs/agent-resources/vercel-mcp",
    authMode: "oauth",
  },
  {
    id: "bocha-mcp",
    name: "博查搜索",
    category: "数据与搜索",
    kind: "MCP",
    description: "使用博查官方 MCP 部署说明配置自己的搜索服务和 API Key。",
    website: "https://github.com/BochaAI/bocha-search-mcp",
  },
  {
    id: "opendata",
    name: "及刻智能·时空数据MCP",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "通过自然语言查询线下时空数据，提供区域热力、场景识别、客流分析、POI查询等能力，助力商业洞察、开店选址等应用场景。",
  },
  {
    id: "qcc-mcp",
    name: "企查查",
    category: "数据与搜索",
    kind: "MCP",
    description: "在企查查 MCP 控制台选定数据服务，复制对应的地址和 API Key。",
    website:
      "https://b-r.qcc.com/portal/information-center/articles/1941128a76eecad4d0d2bc74ca1247d3",
    authMode: "api_key",
  },
  {
    id: "qixin-mcp",
    name: "启信慧眼",
    category: "数据与搜索",
    kind: "MCP",
    description: "通过启信慧眼官方远程 MCP 查询企业工商和风险数据。",
  },
  {
    id: "sq-company-dynamic",
    name: "上奇产业通-企业动态追踪",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "实时追踪企业全生命周期动态，覆盖投资设立、股权变动、创新平台、中标、知识产权、排名、迁移、资质认定、工商变更、上市、标准制定、招聘、拿地等维度，并提供企业实体匹配。",
  },
  {
    id: "shuidi-mcp",
    name: "水滴信用",
    category: "数据与搜索",
    kind: "MCP",
    description: "通过水滴信用远程 MCP 查询企业风险信息。",
  },
  {
    id: "cisp-mcp",
    name: "水滴征信",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "通过水滴征信企业信息服务平台查询工商、知识产权、舆情、财务、土地、企业关联关系、关联方等企业多维信息，并支持企业二要素/三要素核验。",
  },
  {
    id: "xingtu-claw-risk",
    name: "天创信用星图MCP",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "企业风险 AI 分析助手，用自然语言识别企业所属行业、分析企业间关联方关系（股权/实控人/担保/共同投资）。",
  },
  {
    id: "tyc-mcp",
    name: "天眼查",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "通过天眼查 MCP 查询多维度企业数据。支持工商登记、股东结构、司法风险、知识产权、董监高、经营数据等 160+ 项企业数据能力，用自然语言完成企业尽调与商业情报分析。",
  },
  {
    id: "kuaicha-search",
    name: "同花顺快查企业数据",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "查询中国企业工商、股权投资、经营司法风险、知识产权、招投标及新闻舆情数据。",
  },
  {
    id: "yonyou-company",
    name: "用友工商大数据",
    category: "数据与搜索",
    kind: "MCP",
    description: "通过用友工商大数据远程 MCP 查询企业工商信息。",
  },
  {
    id: "amplitude",
    name: "Amplitude",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "将 Agent 连接到 Amplitude MCP,查询产品分析数据、查看事件并生成图表。",
    endpoint: "https://mcp.amplitude.com/mcp",
    website:
      "https://registry.modelcontextprotocol.io/v0.1/servers/com.amplitude%2Fmcp-server/versions/latest",
    authMode: "oauth",
  },
  {
    id: "clickhouse",
    name: "ClickHouse",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 ClickHouse MCP,浏览数据库、运行只读 SQL 并跟踪 Cloud 服务。",
  },
  {
    id: "hex",
    name: "Hex",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "在 Hex Team 或 Enterprise 工作区中检索项目；工具权限由成员角色决定。",
    endpoint: "https://app.hex.tech/mcp",
    authMode: "oauth",
    website: "https://learn.hex.tech/docs/api-integrations/mcp-server",
    useCases: ["查找分析项目", "浏览数据连接"],
    prompts: ["列出与留存分析有关的项目"],
  },
  {
    id: "mixpanel",
    name: "Mixpanel",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "默认连接美国区。欧盟和印度区请在来源文档中选择工作区对应的服务器地址。",
    endpoint: "https://mcp.mixpanel.com/mcp",
    authMode: "oauth",
    website: "https://docs.mixpanel.com/docs/mcp",
    useCases: ["查询项目数据", "分析指标变化"],
    prompts: ["查看本周活跃用户的变化"],
  },
  {
    id: "posthog",
    name: "PostHog",
    category: "数据与搜索",
    kind: "MCP",
    description:
      "将 Agent 连接到 PostHog MCP,查询产品分析数据、管理功能开关并排查线上报错。",
    endpoint: "https://mcp.posthog.com/mcp",
    website: "https://posthog.com/docs/model-context-protocol",
    authMode: "oauth",
  },
  {
    id: "eastmoney-mx",
    name: "东方财富妙想",
    category: "金融投资",
    kind: "MCP",
    description: "通过东方财富妙想远程 MCP 查询行情、资讯和投研数据。",
  },
  {
    id: "gf-etf-mcp",
    name: "广发证券 ETF",
    category: "金融投资",
    kind: "MCP",
    description: "通过广发证券远程 MCP 查询 ETF 榜单和相关数据。",
  },
  {
    id: "gildata",
    name: "恒生聚源 MCP",
    category: "金融投资",
    kind: "MCP",
    description:
      "连接恒生聚源 MCP，查询金融结构化数据、研究报告、公司公告、新闻资讯、条件选股、宏观行业、工商企业数据。支持基金经理观点持仓一致性分析、行业速报生成、金融资讯热点解读。",
  },
  {
    id: "finenter",
    name: "进门投研",
    category: "金融投资",
    kind: "MCP",
    description:
      "覆盖券商、上市公司及资管机构的公开路演内容，整合内外资研报与券商点评等机构级观点，并提供实时行情、财务及量化因子等数据。",
  },
  {
    id: "westock-mcp",
    name: "腾讯自选股",
    category: "金融投资",
    kind: "MCP",
    description:
      "直连腾讯自选股，实时掌握毫秒级行情与资金动态，用自然语言分析自选数据、设置股价提醒、管理模拟交易，轻松搞定盯盘与投资决策。",
  },
  {
    id: "tdx-connector",
    name: "通达信",
    category: "金融投资",
    kind: "MCP",
    description:
      "通过通达信 MCP 查询全球股票行情数据、条件选股、研究报告、公告资讯和宏观信息。支持个股基本面分析、同行业对比和智能选股筛查。",
  },
  {
    id: "datayes-mcp",
    name: "通联数据",
    category: "金融投资",
    kind: "MCP",
    description: "通过通联数据远程 MCP 查询行情、基本面和另类数据。",
  },
  {
    id: "ifind-mcp",
    name: "同花顺 iFinD",
    category: "金融投资",
    kind: "MCP",
    description: "通过同花顺 iFinD 远程 MCP 查询行情、财务和资讯数据。",
  },
  {
    id: "sina-finance",
    name: "新浪财经",
    category: "金融投资",
    kind: "MCP",
    description: "通过新浪财经远程 MCP 查询行情、资讯和财经数据。",
  },
  {
    id: "yingmi-mcp",
    name: "盈米MCP",
    category: "金融投资",
    kind: "MCP",
    description:
      "查询基金与市场数据，完成基金研究、组合分析、财富规划及金融内容生成。",
  },
  {
    id: "shanglv-mcp-gateway",
    name: "用友智能服务（AI BaaS）",
    category: "金融投资",
    kind: "MCP",
    description:
      "通过用友银企联、税企联、商旅云等财务服务产品，为企业提供财务税务与银行资金数据服务，并提供企业商旅运营服务和行程服务。用自然语言完成企业的资金、税务、商旅的全面运营管理。",
  },
  {
    id: "financial-datasets",
    name: "Financial Datasets",
    category: "金融投资",
    kind: "MCP",
    description: "通过 Financial Datasets 远程 MCP 查询美股和金融数据。",
    endpoint: "https://mcp.financialdatasets.ai/",
    website: "https://docs.financialdatasets.ai/mcp-server",
    authMode: "oauth",
  },
  {
    id: "gangtise-mcp",
    name: "Gangtise投研",
    category: "金融投资",
    kind: "MCP",
    description:
      "Gangtise MCP汇聚机构级观点，研报，日程等另类数据，提供投研AI Agent预生成数据及全球行情/财务/估值/宏观行业等结构化数据。",
  },
  {
    id: "mercury",
    name: "Mercury",
    category: "金融投资",
    kind: "MCP",
    description: "连接 Mercury 账号并选择允许访问的权限。",
    endpoint: "https://mcp.mercury.com/mcp",
    authMode: "oauth",
    website: "https://docs.mercury.com/docs/connecting-mercury-mcp",
    useCases: ["查看交易", "检索账户信息"],
    prompts: ["列出最近一周的交易"],
  },
  {
    id: "ramp",
    name: "Ramp",
    category: "金融投资",
    kind: "MCP",
    description:
      "连接 Ramp 生产环境。自定义客户端需向 Ramp 登记 OAuth 回调地址。",
    endpoint: "https://mcp.ramp.com/mcp",
    authMode: "oauth",
    website: "https://docs.ramp.com/developer-api/v1/guides/ramp-mcp-remote",
    useCases: ["查询公司支出"],
    prompts: ["汇总本月的公司支出"],
  },
  {
    id: "stripe",
    name: "Stripe",
    category: "金融投资",
    kind: "MCP",
    description: "将智能体接入 Stripe MCP,查询支付、客户与余额,并处理退款。",
    endpoint: "https://mcp.stripe.com",
    website: "https://docs.stripe.com/mcp",
    authMode: "oauth",
  },
  {
    id: "tushare-mcp",
    name: "Tushare",
    category: "金融投资",
    kind: "MCP",
    description: "通过 Tushare 官方远程 MCP 查询金融数据。",
  },
  {
    id: "wind-finance",
    name: "Wind 金融数据",
    category: "金融投资",
    kind: "MCP",
    description:
      "通过自然语言查询股票、基金、指数、债券、公告、财经新闻和宏观经济数据。",
  },
  {
    id: "fanruan-growth-advisor",
    name: "帆软增长谋士",
    category: "营销增长",
    kind: "MCP",
    description:
      "企业洞察与增长分析：查询企业经营风险、舆情、招投标、专利、资质、政策等公开商业数据，并调用已授权的 MOSS 增长谋士 Agent 生成分析与报告。",
  },
  {
    id: "oceanengine-mcp",
    name: "巨量引擎",
    category: "营销增长",
    kind: "MCP",
    description: "通过巨量引擎官方远程 MCP 查询广告投放与营销数据。",
  },
  {
    id: "tiktok-ads",
    name: "TikTok for Business",
    category: "营销增长",
    kind: "MCP",
    description: "通过 TikTok for Business 官方远程 MCP 查询广告投放数据。",
  },
  {
    id: "weisheng-scrm",
    name: "微盛企微管家SCRM",
    category: "销售与客户",
    kind: "MCP",
    description:
      "查询或管理企业微信中的客户信息、客户标签、客户群、营销素材、活码、群发、跟进记录、联系人、商机、汇报、抽奖、客户日程、聊天记录等业务能力。",
  },
  {
    id: "xiaoshouyi-mcp",
    name: "销售易",
    category: "销售与客户",
    kind: "MCP",
    description: "通过销售易官方远程 MCP 查询和操作 CRM 数据。",
  },
  {
    id: "youshu-bd-mate",
    name: "有数智客 · 对公(To B)营销助手",
    category: "销售与客户",
    kind: "MCP",
    description:
      "对公营销助手是基于企业全维数据构建的对公营销智能助手，提供从客户洞察、访前准备、话术生成到产品匹配的完整展业闭环能力。",
  },
  {
    id: "attio",
    name: "Attio",
    category: "销售与客户",
    kind: "MCP",
    description: "在 Attio 授权当前工作区，检索联系人、公司和销售记录。",
    endpoint: "https://mcp.attio.com/mcp",
    authMode: "oauth",
    website: "https://attio.com/apps/mcp",
    useCases: ["查找联系人", "查看销售管道"],
    prompts: ["查找这家公司的联系人和跟进记录"],
  },
  {
    id: "hubspot",
    name: "HubSpot",
    category: "销售与客户",
    kind: "MCP",
    description:
      "在 HubSpot 创建 MCP 连接器并登记回调地址，使用其 OAuth 客户端凭据访问 CRM。",
    endpoint: "https://mcp.hubspot.com",
    authMode: "oauth",
    website:
      "https://developers.hubspot.com/docs/apps/developer-platform/build-apps/integrate-with-the-remote-hubspot-mcp-server",
    useCases: ["检索 CRM 记录"],
    prompts: ["列出这个客户的联系人"],
  },
  {
    id: "intercom",
    name: "Intercom",
    category: "销售与客户",
    kind: "MCP",
    description:
      "检索有权访问的 Intercom 对话与联系人。请核对工作区受支持的区域。",
    endpoint: "https://mcp.intercom.com/mcp",
    authMode: "oauth",
    website: "https://developers.intercom.com/docs/guides/mcp",
    useCases: ["检索客服对话", "查找联系人"],
    prompts: ["查找与订单延迟有关的客服对话"],
  },
  {
    id: "infimind-ecommerce-image",
    name: "极睿电商生图",
    category: "电商零售",
    kind: "MCP",
    description:
      "通过 MCP 生成 KOC 种草拼图、商品主图与电商商品详情页，并查询任务生成结果。",
  },
  {
    id: "infimind-video",
    name: "极睿视频",
    category: "电商零售",
    kind: "MCP",
    description:
      "通过自然语言使用极睿视频的专业视频生成和视频分析能力，支持多素材参考、任务进度查询与结构化分析结果。",
  },
  {
    id: "lingxing-mcp",
    name: "领星ERP",
    category: "电商零售",
    kind: "MCP",
    description:
      "使用自然语言查询和管理领星ERP中的店铺、库存、Listing、销售、利润、广告和运营数据。",
  },
  {
    id: "picset-commerce-images",
    name: "Picset AI 电商图片",
    category: "电商零售",
    kind: "MCP",
    description:
      "Picset AI 面向电商卖家、设计师和品牌团队，提供设计并制作电商套图、商品主图和商品详情图等。适用于设计制作电商套图、商品套图、主图、详情图、电商图、Listing 图、上架图、卖点图、功能图、场景图、详情长图或长详情页等商品图片需求时使用。适配 Amazon、淘宝、天猫、1688、Temu、TikTok Shop、拼多多、抖音电商、OZON、独立站、Shopify、Shopee、阿里巴巴国际站、Alibaba.com、速卖通、AliExpress、SHEIN、京东等国内国际主流电商平台",
  },
  {
    id: "picset-video-generation",
    name: "Picset AI 视频生成",
    category: "电商零售",
    kind: "MCP",
    description:
      "Picset AI 视频生成连接器支持在 WorkBuddy 中通过 Agent SK 调用 Picset AI 视频生成能力，完成商品参考图上传登记、视频积分预估、确认提交、异步生成和任务状态查询。MVP 支持 Seedance 2.0、Seedance 2.0 Fast、Seedance 2.0 Mini 三个模型，默认使用 Seedance 2.0，适合基于商品素材和文字需求快速生成电商短视频。",
  },
  {
    id: "moka-hr",
    name: "Moka HR",
    category: "人力招聘",
    kind: "MCP",
    description: "通过 Moka 官方远程 MCP 查询招聘和 HR 数据。",
  },
  {
    id: "pkulaw-mcp",
    name: "北大法宝",
    category: "法律合规",
    kind: "MCP",
    description: "通过北大法宝远程 MCP 检索法律法规和案例。",
  },
  {
    id: "fayan-mcp",
    name: "法研法规",
    category: "法律合规",
    kind: "MCP",
    description: "通过法研远程 MCP 检索法律法规。",
  },
  {
    id: "handaas-patent",
    name: "旷湖专利",
    category: "法律合规",
    kind: "MCP",
    description: "通过旷湖专利大数据远程 MCP 检索专利信息。",
  },
  {
    id: "mozlen-mcp",
    name: "摩知轮",
    category: "法律合规",
    kind: "MCP",
    description: "通过摩知轮远程 MCP 检索法律知识。",
  },
  {
    id: "fazhi-law",
    name: "同花顺法律AI助手",
    category: "法律合规",
    kind: "MCP",
    description:
      "查询中国法律法规、司法案例、裁判文书及互联网法律实务资讯，支持法条检索、类案检索、深度法律研究、法律文书起草与诉讼可视化。",
  },
  {
    id: "wkinfo-mcp",
    name: "威科先行",
    category: "法律合规",
    kind: "MCP",
    description: "通过威科先行远程 MCP 检索法律和合规资料。",
  },
  {
    id: "yuandian-mcp",
    name: "元典",
    category: "法律合规",
    kind: "MCP",
    description: "通过元典远程 MCP 检索中国法律信息。",
  },
  {
    id: "zhihuiya-patent",
    name: "智慧芽专利分析",
    category: "法律合规",
    kind: "MCP",
    description: "通过智慧芽专利分析远程 MCP 检索和分析专利。",
  },
  {
    id: "baidu-map",
    name: "百度地图",
    category: "出行生活",
    kind: "MCP",
    description:
      "在百度地图开放平台创建服务端 AK，填写包含自己 AK 的完整 Streamable HTTP 地址。",
    endpointExample: "https://mcp.map.baidu.com/mcp?ak=<YOUR_AK>",
    website: "https://lbsyun.baidu.com/docs/ai?title=mcpserver/quickstart",
  },
  {
    id: "caocao-mcp",
    name: "曹操出行",
    category: "出行生活",
    kind: "MCP",
    description: "通过曹操出行官方远程 MCP 查询出行与运力相关能力。",
  },
  {
    id: "didi-mcp",
    name: "滴滴出行",
    category: "出行生活",
    kind: "MCP",
    description:
      "在滴滴开发者控制台激活 MCP Key，填写包含自己 Key 的生产环境地址。",
    endpointExample:
      "https://mcp.didichuxing.com/mcp-servers?key=<YOUR_API_KEY>",
    website: "https://mcp.didichuxing.com/api",
  },
  {
    id: "variflight-mcp",
    name: "飞常准",
    category: "出行生活",
    kind: "MCP",
    description: "使用飞常准控制台签发的个人 MCP Key 查询航班动态和行程。",
    endpoint: "https://c-gw.variflight.com/chat_message/mcp/api",
    authMode: "api_key",
    website: "https://cmcp.variflight.com/",
    useCases: ["查航班动态", "查机场信息"],
    prompts: ["查一下这个航班的计划起飞时间"],
  },
  {
    id: "amap-maps",
    name: "高德地图",
    category: "出行生活",
    kind: "MCP",
    description:
      "使用高德开放平台的 Web 服务 Key，填写包含自己 Key 的完整 Streamable HTTP 地址。",
    endpointExample: "https://mcp.amap.com/mcp?key=<YOUR_KEY>",
    website: "https://lbs.amap.com/api/mcp-server/gettingstarted",
  },
  {
    id: "tripnow-mcp",
    name: "航班管家",
    category: "出行生活",
    kind: "MCP",
    description:
      "通过航班管家 TripNow 远程 MCP 查询机票、火车票和航铁实时动态。",
  },
  {
    id: "mcd-mcp",
    name: "麦当劳",
    category: "出行生活",
    kind: "MCP",
    description: "在麦当劳中国开放平台申请 MCP Token 后连接。",
    endpoint: "https://mcp.mcd.cn",
    authMode: "api_key",
    website: "https://open.mcd.cn/mcp/doc",
    useCases: ["查询活动日历"],
    prompts: ["查看本周有哪些活动"],
  },
  {
    id: "tencent-map",
    name: "腾讯地图",
    category: "出行生活",
    kind: "MCP",
    description:
      "通过腾讯位置服务的云开发模板部署，并填写自己实例的 MCP 地址和凭据。",
    website: "https://tcb.cloud.tencent.com/mcp-server/mcp-tencent-map",
  },
  {
    id: "ctrip-wendao",
    name: "携程问道",
    category: "出行生活",
    kind: "MCP",
    description:
      "通过携程问道 API 获取旅行规划与攻略。支持酒店查询、机票搜索、景点推荐、行程规划、美食住宿攻略。",
  },
  {
    id: "weather-cn",
    name: "中国天气",
    category: "出行生活",
    kind: "MCP",
    description: "通过中国天气远程 MCP 查询天气和气象数据。",
  },
  {
    id: "google-maps",
    name: "Google Maps",
    category: "出行生活",
    kind: "MCP",
    description:
      "在 Google Cloud 启用 Maps Grounding Lite，使用项目 API Key 查询地点、天气和路线。",
    endpoint: "https://mapstools.googleapis.com/mcp",
    authMode: "api_key",
    website: "https://developers.google.com/maps/ai/grounding-lite",
    useCases: ["搜索地点", "规划路线"],
    prompts: ["查一下从车站到指定地点的路线"],
    authHeader: "X-Goog-Api-Key",
  },
  {
    id: "zhihuiya-pharma",
    name: "智慧芽医药",
    category: "医疗健康",
    kind: "MCP",
    description: "通过智慧芽医药情报远程 MCP 检索药品和医药情报。",
  },
  {
    id: "dcs-cloud",
    name: "DCS Cloud",
    category: "医疗健康",
    kind: "MCP",
    description:
      "通过自然语言管理 DCS Cloud：项目、任务、数据文件。用户需在云平台个人中心创建个人访问令牌(PAT)并粘贴到连接器，连接器用它登录 dcs CLI。",
  },
  {
    id: "canva",
    name: "Canva",
    category: "内容与设计",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 Canva MCP,查找设计稿、读取其内容并新建设计。 自定义客户端需先在 Canva Developer Portal 开启 Canva MCP，登记回调地址并取得 OAuth 凭据。",
    endpoint: "https://mcp.canva.com/mcp",
    website: "https://www.canva.dev/docs/apps/mcp/access/",
    authMode: "oauth",
  },
  {
    id: "gamma-mcp",
    name: "Gamma",
    category: "内容与设计",
    kind: "MCP",
    description:
      "连接 Gamma 生成并查找演示文稿。自定义客户端需向 Gamma 登记回调地址。",
    endpoint: "https://mcp.gamma.app/mcp",
    authMode: "oauth",
    website: "https://developers.gamma.app/mcp/gamma-mcp-server.md",
    useCases: ["查找主题", "生成演示文稿"],
    prompts: ["列出适合季度汇报的主题"],
  },
  {
    id: "consensus-mcp",
    name: "Consensus",
    category: "教育科研",
    kind: "MCP",
    description: "在 Consensus 检索同行评审的研究论文并获取出处。",
    endpoint: "https://mcp.consensus.app/mcp",
    authMode: "oauth",
    website: "https://docs.consensus.app/docs/mcp",
    useCases: ["检索论文", "整理研究依据"],
    prompts: ["找出关于睡眠与记忆的研究并列出出处"],
  },
  {
    id: "bazhuayu-mcp",
    name: "八爪鱼 RPA",
    category: "企业服务与 IT",
    kind: "MCP",
    description: "通过八爪鱼 RPA 官方远程 MCP 运行采集任务。",
  },
  {
    id: "qingflow-mcp",
    name: "轻流",
    category: "企业服务与 IT",
    kind: "MCP",
    description: "通过轻流官方远程 MCP 查询和操作轻流应用数据。",
  },
  {
    id: "n8n",
    name: "n8n",
    category: "企业服务与 IT",
    kind: "MCP",
    description:
      "从 n8n 实例的 MCP 连接详情中复制 URL，使用实例配置的 OAuth 或访问令牌。",
    website: "https://docs.n8n.io/connect/connect-to-n8n-mcp-server.md",
    authMode: "oauth",
  },
  {
    id: "wiz",
    name: "Wiz",
    category: "企业服务与 IT",
    kind: "MCP",
    description:
      "将你的 Agent 连接到 Wiz MCP,排查云安全风险、漏洞结果与云资源清单。",
  },
];
