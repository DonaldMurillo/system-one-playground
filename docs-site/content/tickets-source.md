# Tickets CLI source

```text
# A file-based CLI. Only triage --criterion urgent makes runtime Jev requests.
expect ticket with:
  id as integer
  team as text
  status as text
  priority as integer
  message as text

command tickets:
  describe "Import, triage, and report on support tickets"

  command import:
    describe "Validate a JSON ticket array and save a normalized copy"
    argument source as file
    option output as file default "tickets.json"

    read source as json called tickets
    require each ticket in tickets matches ticket
    save tickets as json in output
      on existing stop

  command triage:
    describe "Select open tickets using an explicit criterion"
    argument source as file
    option criterion as text choices "urgent", "all" default "urgent"
    option output as file default "selected.json"

    read source as json called tickets
    require each ticket in tickets matches ticket
    keep tickets where status is "open"
    when criterion is "urgent":
      keep tickets where jev:
        ask "Does this ticket describe an active service outage or active incorrect customer charges requiring immediate action?"
        using message
        accept probability at least 0.85
        on uncertain discard
        on failure stop with "Could not evaluate the ticket"
    save tickets as json in output
      on existing stop

  command report:
    describe "Write a numbered JSON report for each team"
    argument source as file
    option by as text choices "team" default "team"
    option output as folder default "./ticket-reports"

    read source as json called tickets
    require each ticket in tickets matches ticket
    group tickets by team called teams
    sort teams by key ascending
    create folder output if missing
    make reports as empty list
    for each team in teams numbered from 1:
      make report with:
        team from team.key
        count from count of team.items
        tickets from team.items
      append report to reports
      save report as json under output named "{number}.json"
        on existing stop
    show reports as table with team, count

```
