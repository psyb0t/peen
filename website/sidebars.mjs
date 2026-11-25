// scripts/make/docs_prepare.py builds the tree from each page's parent and nav_order.
// docusaurus-plugin-openapi-docs writes .content/api/sidebar.ts from api/api.yml,
// and its endpoint pages nest under the REST API page.
import { readFileSync } from "node:fs";
import endpoints from "./.content/api/sidebar.ts";

const REST_PAGE = "http-api";
const REST_LABEL = "REST API";

const sidebar = JSON.parse(readFileSync(new URL("./.content/sidebar.json", import.meta.url), "utf8"));

if (!sidebar.includes(REST_PAGE)) {
  throw new Error(`the sidebar has no top-level ${REST_PAGE} page to hold the REST reference`);
}

export default {
  docs: sidebar.map((item) => item === REST_PAGE
    ? { type: "category", label: REST_LABEL, link: { type: "doc", id: REST_PAGE }, items: endpoints }
    : item),
};
