# SOP: Driving the Studio SDK board (file → dispatch → merge)

How work actually reaches Pilot's executor. The [authoring-a-connector](../integrations/authoring-a-connector.md)
SOP covers the *code* of a port; this covers the *operational loop* that gets
that port built. Pilot does not watch this repo's issue list directly — it
drives off the **GH Project "Studio SDK"** (org project #1,
`github.com/orgs/qf-studio/projects/1`).

---

## The one thing that trips everyone up

**Intake is gated on the board `Status` field, NOT on the `pilot` label.**

```
project_board:
  source_enabled: true
  source_status: Todo      # daemon only ingests cards at Status = "Todo"
```

Filing an issue with the `pilot` label lands a card on the board at
**`Status=NONE`** (the auto-add workflow default). The daemon will sit **idle**
on it forever. It only dispatches once the card is moved to **`Todo`**.

> If "Pilot is idle" — check board Status *first*. `NONE` = never dispatched.

---

## Dispatch lifecycle (the states a card moves through)

| Status | Set by | Meaning |
| --- | --- | --- |
| `NONE` | auto-add workflow (on file) | on board, **not** dispatched |
| `Todo` | **you** (the trigger) | daemon will ingest within ~1–2 poll cycles (30s) |
| `In Progress` | daemon | executing; issue also gets `pilot-in-progress` label |
| `In Review` | daemon | PR opened, awaiting merge |
| `Done` | you (after manual merge) | complete |
| `Blocked` | daemon | failed |

---

## Flipping a card to Todo

The local `gh` token is **`read:project` only** — it cannot edit board fields
(`gh project item-edit` will fail auth). Two ways that work:

1. **Dashboard / Project UI** — drag the card from no-status into **Todo**.
2. **Daemon PAT via GraphQL** — the token in `~/.pilot/config.yaml` (github
   block) has `project` write. Resolved IDs for this board:
   - project: `PVT_kwDOD34yzs4BZIGz`
   - Status field: `PVTSSF_lADOD34yzs4BZIGzzhUJB8o`
   - Todo option: `0b0c1283`

   ```bash
   TOK=$(awk '/^    github:/{f=1} f&&/token:/{print $2; exit}' ~/.pilot/config.yaml)
   PROJ="PVT_kwDOD34yzs4BZIGz"
   # item id for issue <N>:
   ITEM=$(curl -sS -H "Authorization: bearer $TOK" -H "Content-Type: application/json" \
     -d "{\"query\":\"query{node(id:\\\"$PROJ\\\"){... on ProjectV2{items(first:100){nodes{id content{... on Issue{number}}}}}}}\"}" \
     https://api.github.com/graphql | jq -r '.data.node.items.nodes[]|select(.content.number==<N>)|.id')
   # set Status=Todo:
   curl -sS -H "Authorization: bearer $TOK" -H "Content-Type: application/json" \
     -d "{\"query\":\"mutation{updateProjectV2ItemFieldValue(input:{projectId:\\\"$PROJ\\\",itemId:\\\"$ITEM\\\",fieldId:\\\"PVTSSF_lADOD34yzs4BZIGzzhUJB8o\\\",value:{singleSelectOptionId:\\\"0b0c1283\\\"}}){projectV2Item{id}}}\"}" \
     https://api.github.com/graphql
   ```

---

## Per-connector loop (with the 2-issue recipe)

1. **File 2 issues** (`gh issue create -R qf-studio/studio-sdk`), labels
   `pilot,no-decompose`: client/data first, behavior second. Keep specs lean
   (≤4 checkboxes, no epic keywords). See [extraction recipe](#) memory +
   the authoring SOP for scope.
2. **Serialize:** move only the **first** issue to `Todo`. Leave the dependent
   follow-up at `NONE` — the status gate holds it. Add the `backlog` label as
   belt-and-suspenders (and `gh issue edit … --remove-label pilot`).
3. Daemon flips it `In Progress`, implements, opens a **scoped PR**, flips
   `In Review`.
4. **Review against the acceptance gates** (authoring SOP), then **manual merge**
   — `gh pr merge <n> --rebase --delete-branch`. Large PRs otherwise hit the
   stage approval-misconfig (upstream pilot#2598).
5. **Clean the board:** move the issue card to `Done`; delete the no-status PR
   card autopilot adds.
6. **Unblock the follow-up:** set it `Todo` (+ swap `backlog`→`pilot`), repeat
   from step 3.

---

## "Pilot is idle" checklist

1. **Board Status** — is the card `NONE`? → move to `Todo` (above). #1 cause.
2. **Daemon** — exactly one `pilot start` process? (`ps -ef | grep 'pilot start'`).
   A wedged duplicate blocks the workboard.
3. **Token** — github PAT valid + scoped `project, read:org, repo`?
   `curl -sS -I -H "Authorization: token $TOK" https://api.github.com/user`
   → expect `200` + those `x-oauth-scopes` (don't print the token).
4. **Labels** — issue has `pilot` (not just `backlog`) and is `OPEN`.
