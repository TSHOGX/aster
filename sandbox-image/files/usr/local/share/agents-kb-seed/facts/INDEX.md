# Knowledge base index

Read `/agents/CONSTITUTION.md` first. This image supplies a portable bootstrap,
not a deployment's data catalog or business instructions.

- `skills/matplotlib-cjk-fonts/SKILL.md`: rendering CJK text in plots and images.
- `skills/<name>/SKILL.md`: deployment-provided or reviewed procedures. Prefer
  `status: in-use`, treat `observing` as provisional, and avoid `deprecated`.
- `facts/`: reviewed knowledge published into this snapshot.
- `data/`: deployment-private reference material, if explicitly provisioned.

Search the actual mounted snapshot before using a data source. Do not assume a
database, table, credential, object store or domain corpus exists merely because
an example template ships with the source. Ask for missing context rather than
inventing endpoints or switching to an unauthorized data source.
