package main

import "github.com/DonaldMurillo/system-one-playground/internal/semcore"

type RuleSpec = semcore.RuleSpec
type WhereSpec = semcore.WhereSpec
type CheckSpec = semcore.CheckSpec
type AskSpec = semcore.AskSpec
type RuleFile = semcore.RuleFile

var Compile = semcore.Compile
var LoadRuleFile = semcore.LoadRuleFile
var languageProfiles = semcore.LanguageProfiles
