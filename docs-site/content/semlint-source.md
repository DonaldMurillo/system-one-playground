# SOS semlint source

The executable example is `examples/sos/semlint/scan.sos`. Read the [guide](/docs/semlint-sos) for usage and parity notes.

```text
# Source discovery and judgment policies are SOS; source mechanics are shared with Go semlint.
import "std/files" as files
import "std/path" as path
import "std/source" as source
import "std/jev" as jev
import "std/io" as io
import "std/process" as process
import "std/text" as text
import "std/list" as list

to load_battery with sets, rules_file, only:
  make battery []
  when sets is not "none":
    call source.builtin with sets called battery
  when rules_file is not "":
    read rules_file as text called custom_text
    call source.rules with custom_text called custom
    for each override in custom:
      keep battery where id is not override.id
      append override to battery
  when only is not "":
    keep battery where id is only
  when count of battery is 0:
    call io.error with "No rules selected"
    call io.exit with 2
  return battery

to prepare with root, battery, diff_file, since, isolate_files:
  make changed null
  make untracked []
  when diff_file is not "" and since is not "":
    call io.error with "Choose diff_file or since, not both"
    call io.exit with 2
  when diff_file is not "":
    when diff_file is "-":
      call io.read with 8388608 called patch
    otherwise:
      read diff_file as text called patch
    call source.diff with patch, root called changed
  when since is not "":
    call process.run with "git", ["-C", root, "diff", "--no-ext-diff", "--unified=0", since, "--"] called git_result
    when git_result.status is not 0:
      call io.error with git_result.stderr
      call io.exit with 2
    call source.diff with git_result.stdout, root called changed
    call process.run with "git", ["-C", root, "ls-files", "--others", "--exclude-standard", "-z"] called git_untracked
    when git_untracked.status is not 0:
      call io.error with git_untracked.stderr
      call io.exit with 2
    call text.split with git_untracked.stdout, "\u0000" called untracked
  call files.discover with root, [".git", "node_modules", "vendor", "dist", "testdata"] called paths
  make units []
  for each relative in paths:
    call path.join with root, relative called filename
    call source.supported with filename called supported
    when supported:
      read filename as text called code
      call source.extract with filename, code, battery called extracted
      for each unit in extracted:
        append unit to units
  make selected []
  for each unit in units:
    make include true
    when changed is not null:
      call source.touches with changed, unit.file, unit.start_line, unit.end_line called include
      when since is not "":
        call path.relative with root, unit.file called relative
        when untracked contains relative:
          assign include true
    when include:
      append unit to selected
  make prepared []
  when isolate_files:
    group selected by file called groups
    for each group in groups:
      call source.prepare with group.items, battery called file_units
      for each unit in file_units:
        append unit to prepared
  otherwise:
    call source.prepare with selected, battery called prepared
  make result with:
    units from prepared
    changed from changed
    untracked from untracked
  return result

to judge_unit with unit, calibrate:
  make findings []
  when count of unit.questions > 0:
    call jev.questions with unit.questions called questions
    evaluate unit.state by jev using questions called answers
      on failure:
        when failure.kind is "input_too_large":
          call source.reduce with unit.state called reduced
          evaluate reduced by jev using questions called answers
        otherwise:
          rethrow
    for each rule in unit.rules:
      call jev.answer with answers, rule.id called answer
      make confidence 0
      make fires false
      when rule.type is "noul":
        assign confidence answer.p_yes
        assign fires confidence >= rule.threshold
      otherwise:
        assign confidence answer.confidence
        assign fires answer.expected >= rule.at_least and confidence >= rule.min_confidence
      when calibrate:
        assign fires true
      when fires:
        make finding with:
          file from unit.file
          line from rule.line
          unit_start from unit.start_line
          unit_end from unit.end_line
          rule from rule.id
          severity from rule.severity
          message from rule.message
          why from rule.why
          confidence from confidence
          snippet from rule.snippet
          answer from answer
        append finding to findings
  return findings

to separation_result with rule_id, positive, negative:
  make gap 0
  make robust_gap 0
  make suggested null
  make verdict "UNTESTED"
  when count of positive > 0 and count of negative > 0:
    call list.percentile with positive, 0 called lowest_true
    call list.percentile with negative, 100 called highest_false
    call list.percentile with positive, 10 called true_edge
    call list.percentile with negative, 90 called false_edge
    assign gap lowest_true - highest_false
    assign robust_gap true_edge - false_edge
    assign suggested (true_edge + false_edge) / 2
    assign verdict "ROBUST"
    when gap <= 0:
      assign verdict "MOSTLY"
    when robust_gap < 0.30:
      assign verdict "WORKABLE"
    when robust_gap < 0.15:
      assign verdict "FRAGILE"
    when robust_gap <= 0:
      assign verdict "UNUSABLE"
  make result with:
    rule from rule_id
    true_readings from positive
    false_readings from negative
    strict_gap from gap
    robust_gap from robust_gap
    suggested_threshold from suggested
    verdict from verdict
  return result

command semlint:
  describe "Scan repository source and evaluate semantic rules in bounded parallel batches"
  option root as folder default "."
  option sets as text choices "default", "browser-storage", "all", "none" default "default"
  option rules_file as file default ""
  option only as text default ""
  option diff_file as file default ""
  option since as text default ""

  command rules:
    describe "List the active rule definitions without provider calls"
    call load_battery with sets, rules_file, only called battery
    show battery

  command sites:
    describe "Extract candidate units, context, deterministic findings and questions without provider calls"
    call load_battery with sets, rules_file, only called battery
    call prepare with root, battery, diff_file, since, false called prepared
    make units prepared.units
    show units

  command check:
    describe "Judge each source unit once and report findings and skipped failures"
    option workers as integer default 8
    option max_units as integer default 400
    option min_confidence as number default 0
    option severity as text choices "hint", "warning", "error" default "hint"
    switch changed_lines_only default off
    option format as text choices "json", "text", "hints" default "json"
    switch calibrate default off
    switch no_fail default off
    call load_battery with sets, rules_file, only called battery
    call prepare with root, battery, diff_file, since, false called prepared
    make units prepared.units
    when max_units > 0 and count of units > max_units:
      call io.error with "{count of units} units exceed max_units={max_units}; narrow scope or raise the limit"
      call io.exit with 2
    map each unit in units with at most workers running called results collecting failures:
      call judge_unit with unit, calibrate called findings
      return findings
    make findings []
    make skipped []
    for each unit in units:
      for each finding in unit.deterministic_findings:
        append finding to findings
    for each result in results numbered from 0:
      when result.ok:
        for each finding in result.value:
          append finding to findings
      otherwise:
        call list.at with units, number called skipped_unit
        make failure with:
          file from skipped_unit.file
          line from skipped_unit.start_line
          unit_index from number
          error from result.error
        append failure to skipped
    make selected []
    for each finding in findings:
      make include finding.confidence >= min_confidence
      when severity is "error" and finding.severity is not "error":
        assign include false
      when severity is "warning" and finding.severity is "hint":
        assign include false
      when changed_lines_only and prepared.changed is not null:
        call source.touches with prepared.changed, finding.file, finding.line, finding.line called touched
        call path.relative with root, finding.file called relative
        when not touched and not (prepared.untracked contains relative):
          assign include false
      when include:
        append finding to selected
    assign findings selected
    sort findings by line
    sort findings by file
    make report with:
      findings from findings
      skipped from skipped
      units from count of units
    when format is "json":
      show report
    otherwise:
      for each finding in findings:
        show "{finding.file}:{finding.line} [{finding.rule}] {finding.message} ({finding.severity}, p={finding.confidence})"
        when format is "hints":
          show "  consequence: {finding.why}"
      for each skipped_unit in skipped:
        show "SKIPPED {skipped_unit.file}:{skipped_unit.line} {skipped_unit.error.message}"
      show "{count of findings} findings across {count of units} units; {count of skipped} skipped"
    when not no_fail:
      when count of skipped > 0:
        call io.exit with 2
      when count of findings > 0:
        call io.exit with 1


  command separation:
    describe "Measure labelled and correct populations with the full active question battery"
    option workers as integer default 8
    option runs as integer default 3
    option max_units as integer default 400
    when runs < 1:
      call io.error with "runs must be at least 1"
      call io.exit with 2
    call load_battery with sets, rules_file, only called battery
    call prepare with root, battery, diff_file, since, true called prepared
    make units prepared.units
    when max_units > 0 and count of units > max_units:
      call io.error with "{count of units} units exceed max_units={max_units}"
      call io.exit with 2
    make jobs []
    for each unit in units:
      call path.extension with unit.file called extension
      call text.slice with unit.file, 0, count of unit.file - count of extension called stem
      make sidecar "{stem}.expected.json"
      make expectations []
      call files.exists with sidecar called exists
      when exists:
        read sidecar as json called truth
        assign expectations truth.expect
      repeat runs times:
        make job with:
          unit from unit
          expectations from expectations
        append job to jobs
    map each job in jobs with at most workers running called results collecting failures:
      call judge_unit with job.unit, true called findings
      make readings []
      for each finding in findings:
        make positive false
        for each expectation in job.expectations:
          when expectation.rule is finding.rule and expectation.line >= finding.unit_start and expectation.line <= finding.unit_end:
            assign positive true
        make reading with:
          rule from finding.rule
          positive from positive
          probability from finding.confidence
        append reading to readings
      return readings
    make readings []
    make skipped []
    for each result in results numbered from 0:
      when result.ok:
        for each reading in result.value:
          append reading to readings
      otherwise:
        call list.at with jobs, number called job
        make skipped_job with:
          file from job.unit.file
          line from job.unit.start_line
          error from result.error
        append skipped_job to skipped
    group readings by rule called populations
    make measurements []
    for each population in populations:
      make positive []
      make negative []
      for each reading in population.items:
        when reading.positive:
          append reading.probability to positive
        otherwise:
          append reading.probability to negative
      call separation_result with population.key, positive, negative called measurement
      append measurement to measurements
    sort measurements by rule
    make report with:
      measurements from measurements
      skipped from skipped
      runs from runs
    show report
    when count of skipped > 0:
      call io.exit with 2

```
