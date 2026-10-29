# Benchmark and research artifacts

- Store benchmark results, research reports, profiles, experimental artifacts,
  and one-off collectors under the repository-root `research/` directory.
  It is local-only and fully ignored by Git.
- Reuse an existing study directory inside `research/` when appropriate, or
  create a descriptive subdirectory there. Do not create other output directories
  at the repository root or inside source packages.
- Keep reusable benchmark tests, scripts, and required test fixtures alongside
  the source. Set script output defaults to a directory under `research/`.
- Do not force-add research artifacts to Git. Keep task status in `TODO.md`
  concise and link to local reports in `research/` for detailed evidence.
