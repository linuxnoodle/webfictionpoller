package static

import _ "embed"

//go:embed app.css
var CSS []byte

//go:embed htmx.min.js
var HTMX []byte

//go:embed alpine.min.js
var Alpine []byte
