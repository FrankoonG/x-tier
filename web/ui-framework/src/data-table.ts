/* ---------------------------------------------------------------------------
 * @stratum/ui/data-table — separate entry point, on purpose.
 *
 * DataTable is the only component in the library with dependencies of its own:
 * `@tanstack/react-table` (v9) and `@tanstack/react-virtual` (v3).
 *
 * They are declared as OPTIONAL peers, but "optional" is only true if a
 * consumer who never uses this component never has to install them — and that
 * is not achievable from the main barrel. Even with `preserveModules` and a
 * dynamic `import()`, a bundler resolves the specifier while walking the module
 * graph, before tree-shaking can remove the module. Merely re-exporting
 * DataTable from `@stratum/ui` was therefore enough to fail the build of an app
 * that only wanted a Button.
 *
 * Hoisting the specifier into a variable defeats the bundler, but it also
 * defeats the dev server's pre-bundling, so the import fails at runtime even
 * when the package IS installed. That is a worse trade.
 *
 * A subpath export is the clean answer. `@stratum/ui` stays dependency-light
 * for everyone; importing this path is an explicit statement that you want the
 * grid engine and have installed it.
 *
 *   npm i @tanstack/react-table @tanstack/react-virtual
 *   import { DataTable } from '@stratum/ui/data-table';
 *
 * The plain `Table` in the main entry has no dependencies and covers sorting,
 * selection, sticky headers and density. Reach for DataTable only when you
 * need virtualisation, column sizing or the full engine.
 * ------------------------------------------------------------------------- */

export { DataTable } from './data/DataTable/DataTable';
export type { DataTableProps, DataTableColumn, SortRule } from './data/DataTable/DataTable';
