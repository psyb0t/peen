---
paths:
  - "**/*.sql"
---

# SQL

Sources: the OWASP [SQL Injection Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/SQL_Injection_Prevention_Cheat_Sheet.html).

Follow the project's migration tool, naming convention, and existing queries first. Use this rule where the project says nothing.

- In application code, pass every value as a bound parameter. Never build SQL by concatenating or formatting values into the query text.
- Never edit a migration that has already been applied anywhere. Add a new one.
- Give each migration the reverse migration its tool supports, and check that both directions run.
- Run a multi-statement change in a transaction when the database supports transactional DDL.
- Name the columns a query needs instead of `SELECT *` in application queries.
- When a new query filters or joins on a column of a large table, check whether that column needs an index.
- Keep table and column names in the project's existing case and plurality.
