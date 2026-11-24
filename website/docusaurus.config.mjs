const repository = "https://github.com/psyb0t/peen";
const baseUrl = "/peen/";

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
        editUrl: ({ docPath }) => repository + "/edit/main/docs/" + docPath,
      },
    }],
  ],
  themes: [
    "@docusaurus/theme-mermaid",
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
  },
};
