## Current spec and clean cutover

The current spec defines what the system should be, together with its applicable
standards and required evaluation rubrics. Earlier specs, prior agent reports,
and the existing implementation are historical context; they do not preserve
superseded requirements.

Reuse and adapt the existing implementation. Use "Would this belong if we
implemented the current spec today?" to identify obsolete design, not to justify
unnecessary rewrites. Keep supporting details that serve current requirements;
omission from the spec alone does not make them obsolete.

Remove code, fields, interfaces, configuration, and behavior that only served
superseded requirements. Do not wait for the spec to explicitly request their
deletion. Do not retain legacy aliases or compatibility paths merely because
they already exist.

Focus retirement checks on changed requirements and their affected code, data,
and interfaces. Reuse prior independent retirement checks when their evidence
remains sufficient and the relevant spec, implementation, and live state are
unchanged. An implementation agent's report does not replace independent
evaluation.

Preserve required data and history while migrating. Any temporary compatibility
path must have a concrete transition need and a clear removal condition. Do not
present an unfinished transition as the completed design.
