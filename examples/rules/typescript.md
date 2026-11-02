---
paths:
  - "**/*.{ts,tsx,mts,cts}"
  - "**/*.{js,jsx,mjs,cjs}"
---

# TypeScript and JavaScript

Sources: the [TypeScript handbook](https://www.typescriptlang.org/docs/handbook/intro.html) and the [typescript-eslint recommended rules](https://typescript-eslint.io/rules/).

Follow the project's own `tsconfig.json`, ESLint and Prettier settings, and the style of the surrounding code first. Use this rule where the project says nothing.

- Keep `strict` mode on. Do not use `any`. Take `unknown` and narrow it.
- Do not use the non-null assertion `!` to silence the compiler. Check the value.
- Use `const` by default, `let` when the variable changes, and never `var`.
- Compare with `===` and `!==`.
- Handle every promise. Await it, or end the chain with `.catch`. Never leave an empty `catch`.
- Give exported functions explicit parameter and return types.
- Use the module system the project uses, usually `import` and `export`.
- Run the project's type check, such as `tsc --noEmit`, its lint, and its tests before you report the work as done.
- Install packages with the project's package manager, the one its lockfile belongs to. Never edit a lockfile or generated file by hand.
