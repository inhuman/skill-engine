# Fixtures

Deliberately broken skills, one file per class of defect. Placeholder data only
— no real server, tool or skill names.

The expected findings of each file are written out in `fixtures_test.go`, and
the test compares the whole set both ways: a rule that stopped firing has gone
missing, and a rule that fires where it was not expected is the noise that stops
the report being read.

| file | rules | the defect |
|---|---|---|
| `prose-defects.yaml` | S3, S6, E5 | prose reaching for what the installation does not carry |
| `dead-server.yaml` | E1, E2, E3 | the declared radius and the live installation disagree |
| `broken-program.yaml` | W1 | a step that does two things at once — the engine refuses it |
| `silent-loop.yaml` | W2, W6, W12, W14 | a program that runs green and gathers nothing |
| `envelope.yaml` | W3, W8, W10 | a wrapper passed where the payload was meant |
| `loose-schema.yaml` | W9, W13, W16 | a structured answer with nothing holding it together |
| `asset-names.yaml` | W19, W20 | an asset referenced under a name nobody declared, and one nobody reads |
| `dead-alternative.yaml` | W14, W18 | a `contains` dictionary whose entries swallow each other |
| `payload.yaml` | W4, W5, W7, W11, W15, W17, E4 | payloads passed the wrong way, addresses that do not exist |

Two rules have no fixture on purpose: **S1** needs a file that does not load,
and **S5** an oversized one — a fixture cannot be both broken and readable. They
are covered inline in the tests.
