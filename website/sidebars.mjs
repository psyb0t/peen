// scripts/make/docs_prepare.py builds the tree from each page's parent and nav_order.
import { readFileSync } from "node:fs";

const sidebar = JSON.parse(readFileSync(new URL("./.content/sidebar.json", import.meta.url), "utf8"));

export default { docs: sidebar };
