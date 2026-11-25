const repository = "https://github.com/psyb0t/peen";
const baseUrl = "/peen/";
// docs_prepare.py copies api/api.yml here; the plugin writes one page per
// operation into .content/api.
const apiSpec = ".assets/reference/api.yml";
const apiDocsDirectory = "api/";

export default {
  title: "peen",
  tagline: "Coding agent server documentation",
  url: "https://psyb0t.github.io",
  baseUrl,
  trailingSlash: true,
  future: { faster: { rspackBundler: true, swcJsLoader: true } },
  organizationName: "psyb0t",
  projectName: "peen",
  onBrokenLinks: "throw",
  onBrokenAnchors: "throw",
  markdown: { format: "detect", mermaid: true, hooks: { onBrokenMarkdownLinks: "throw" } },
  staticDirectories: [".assets"],
  scripts: [{ src: "https://psyb0t.github.io/analytics.js", async: true }],
  presets: [
    ["classic", {
      blog: false,
      sitemap: { ignorePatterns: [baseUrl + "search", baseUrl + "search/"] },
      theme: { customCss: ["./src/css/theme.css"] },
      docs: {
        path: ".content",
        routeBasePath: "/",
        sidebarPath: "./sidebars.mjs",
        docItemComponent: "@theme/ApiItem",
        editUrl: ({ docPath }) => docPath.startsWith(apiDocsDirectory)
          ? undefined
          : repository + "/edit/main/docs/" + docPath,
      },
    }],
  ],
  plugins: [
    ["docusaurus-plugin-openapi-docs", {
      id: "api",
      docsPluginId: "classic",
      config: {
        peen: {
          specPath: apiSpec,
          outputDir: ".content/" + apiDocsDirectory,
          showInfoPage: false,
          hideSendButton: true,
          sidebarOptions: { groupPathsBy: "tag" },
        },
      },
    }],
  ],
  themes: [
    "@docusaurus/theme-mermaid",
    "docusaurus-theme-openapi-docs",
    ["@easyops-cn/docusaurus-search-local", {
      docsDir: ".content",
      docsRouteBasePath: "/",
      indexBlog: false,
      hashed: true,
    }],
  ],
  themeConfig: {
    navbar: {
      title: "peen",
      items: [
        { href: "https://psyb0t.github.io/projects/", label: "All projects", position: "left" },
        { href: repository + "/releases", label: "Releases", position: "right" },
        { href: repository + "/blob/main/CHANGELOG.md", label: "Changelog", position: "right" },
        { href: repository, label: "GitHub", position: "right" },
      ],
    },
    colorMode: { defaultMode: "dark" },
    mermaid: { theme: { light: "neutral", dark: "dark" } },
    api: { schemaExpansion: { enabled: true, default: 1 } },
  },
};
