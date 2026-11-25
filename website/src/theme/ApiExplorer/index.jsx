import React from "react";
import CodeBlock from "@theme/CodeBlock";

// Each operation shows its curl sample from api/api.yml in place of the
// plugin's in-browser request editor, which would send the reader's token from
// the docs site.
export default function ApiExplorer({ item }) {
  return (item["x-codeSamples"] ?? []).map(({ label, source }) => (
    <CodeBlock key={source} language="bash" title={label}>
      {source}
    </CodeBlock>
  ));
}
