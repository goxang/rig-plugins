# A company as a tree of rig projects

Every unit of a company, a team or the executive office, is its own rig project. A unit reads only
the units directly below it, over their `rig serve`, through one protocol: the `org` plugin's
`org.unit.*` sources and actions. What each layer does with that data, and what its people see and
edit, is up to the layer's own plugin. The protocol is shared; the pages are not.

## Layers

| layer | plugin | tabs | its own records |
|---|---|---|---|
| team | `org-team` (pack) | the team's usual tabs, plus Team | items, requests; exposes sources and actions |
| delivery (product owner, project manager, analyst) | `org-delivery` | Home, Jira, Goals, Reports, Live, Calendar, Roadmap, Team, Confluence, Mail, Notes, Setup | team members, meetings (mail invites), milestones, scheduled team messages, private notes |
| department | `org-management` | Department, Teams, Commitments, Capacity, Requests, Live | commitments |
| product office | `org-product` | Overview, Intake, Portfolio, Roadmap, Specs, Releases, Metrics | initiatives (RICE), intake, specs, releases |
| technology (CTO, IT deputy) | `org-technology` | Scorecard, Live, Units, Programs, Architecture, Risks | programs, ADRs, radar, risks |
| people (HR) | `org-people` | Workforce, Figures, Hiring, Plans, Training, Requests | positions, headcount plans, trainings |
| executive | `org-executive` | Company, OKRs, Decisions, Risks, Briefings | objectives, key results bound to live KPIs, decisions, briefings |
| stakeholders | `org-portal` | Status, Roadmap, Releases, Ask | none: what the units below publish to them |

A unit can enable several (an IT deputy with `org-technology` and `org-executive`). Each plugin
ships a `ui` profile that locks the web UI to its tabs: a manager never sees Operate or Develop.
The project's `ui: {title: ...}` merges over it. Each also ships agent templates for its roles
(product manager, spec writer, risk analyst, chief of staff, ...).

## Protocol

| source / action | what |
|---|---|
| `org.unit.info` | name, title, layer, parent, owner |
| `org.unit.kpis` | the unit's KPIs, or its children's (`child=name`, `child=*`) |
| `org.unit.people` | headcount and figures, the only thing HR reads |
| `org.unit.items` / `org.unit.item` | risks, incidents, highlights, decisions |
| `org.unit.requests` / `org.unit.request` | asks sent down, answered by the unit |
| `org.unit.children` | each child's reachability and roll-up |
| `org.unit.goals` / `org.unit.goal` | goals given down (`to=child`), each measured on one of the child's KPIs against a target (`>= 99.9`) |
| `org.unit.reports` / `org.unit.report` | status, release and retro reports published upward; `child=*` reads the units below's |
| `org.unit.exposed` / `org.unit.show` / `org.unit.invoke` | what a unit below publishes upward |

A unit publishes with `expose:` in its `org` config:

```yaml
expose:
  - {name: payments-flow, title: Live payments, source: flow.graph, widget: graph, refresh: 5s, to: [parent, product]}
  - {name: payments-flow, from: switch/payments-flow, to: [parent, stakeholder]}   # re-export a child's
  - {name: report-incident, action: org.unit.item, args: {kind: incident}, inputs: [title, severity]}
```

The units above get each as a card: a source in its widget, an action as a form. `from:` passes a
child's exposure one level up, so a team's live graph can reach the executive without the
executive ever reading the team.

`org-delivery` works on top of `jira`, `confluence` and `mail`. Each connects from its Setup tab: an
address, a user and an API token (an app password for Gmail), kept in the project's rig secrets.

## Security

- A token of a unit above is an account of a protocol role on the unit's server: `parent`, `hr`,
  `product` or `stakeholder`. Each reaches `org.unit.*` only, never the unit's GitLab, logs or
  databases. A unit's own policy wins over these defaults.
- `hr` reads people figures, not KPIs; `product` reads KPIs, not people; `stakeholder` reads only
  exposures and may run the actions exposed to it.
- An exposure is visible to the roles in its `to:` (default `[parent]`); others get not found.
- An action invoked by `parent` or `product` waits for a person in the unit to approve it.
- A unit sees one level down: drilling into a grandchild is denied unless the child lists the
  role under `drill:`.
- Outside a demo, serve over TLS and keep children's tokens in `rig secret set`.

A working tree of nine units, with a seed script and an access-matrix check, is the pep-org demo.
