package main

import (
	"strconv"
	"github.com/DonaldMurillo/system-one-playground/internal/semcore"
)

type Site = semcore.Site
type Unit = semcore.Unit

var ExtractUnits = semcore.ExtractUnits

func itoa(n int) string { return strconv.Itoa(n) }
