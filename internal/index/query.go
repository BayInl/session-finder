package index

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Query parsing deliberately happens outside SQLite. FTS5 MATCH accepts its own
// operators, so passing a user's complete expression to it would make the
// command's grammar depend on SQLite and would also make punctuation unsafe.
type queryTokenKind uint8

const (
	queryWord queryTokenKind = iota
	queryPhrase
	queryLParen
	queryRParen
	queryOperator
)

type queryToken struct {
	kind  queryTokenKind
	text  string
	raw   string
	pos   int
	quote bool
}

type queryAtom struct {
	value  string
	phrase bool
	key    string
}

type queryExprKind uint8

const (
	queryAtomExpr queryExprKind = iota
	queryNotExpr
	queryAndExpr
	queryOrExpr
)

type queryExpr struct {
	kind        queryExprKind
	atom        queryAtom
	left, right *queryExpr
}

type queryFilters struct {
	tools  []string
	cwds   []string
	afters []string
}

func lexQuery(input string) ([]queryToken, error) {
	tokens := make([]queryToken, 0, 8)
	for i := 0; i < len(input); {
		r, size := utf8.DecodeRuneInString(input[i:])
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
		}
		if unicode.IsSpace(r) {
			i += size
			continue
		}
		if r == '(' {
			tokens = append(tokens, queryToken{kind: queryLParen, raw: input[i : i+size], pos: i})
			i += size
			continue
		}
		if r == ')' {
			tokens = append(tokens, queryToken{kind: queryRParen, raw: input[i : i+size], pos: i})
			i += size
			continue
		}

		start := i
		var value strings.Builder
		hadQuote := false
		hadEscape := false
		for i < len(input) {
			r, size = utf8.DecodeRuneInString(input[i:])
			if r == utf8.RuneError && size == 1 {
				return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
			}
			if unicode.IsSpace(r) || r == '(' || r == ')' {
				break
			}
			if r == '\\' {
				hadEscape = true
				i += size
				if i >= len(input) {
					return nil, fmt.Errorf("dangling escape at byte %d", i-1)
				}
				escaped, escapedSize := utf8.DecodeRuneInString(input[i:])
				if escaped == utf8.RuneError && escapedSize == 1 {
					return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
				}
				value.WriteRune(escaped)
				i += escapedSize
				continue
			}
			if r == '"' {
				hadQuote = true
				i += size
				closed := false
				for i < len(input) {
					quoted, quotedSize := utf8.DecodeRuneInString(input[i:])
					if quoted == utf8.RuneError && quotedSize == 1 {
						return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
					}
					if quoted == '\\' {
						i += quotedSize
						if i >= len(input) {
							return nil, fmt.Errorf("dangling escape at byte %d", i-1)
						}
						escaped, escapedSize := utf8.DecodeRuneInString(input[i:])
						if escaped == utf8.RuneError && escapedSize == 1 {
							return nil, fmt.Errorf("invalid UTF-8 at byte %d", i)
						}
						value.WriteRune(escaped)
						i += escapedSize
						continue
					}
					if quoted == '"' {
						i += quotedSize
						closed = true
						break
					}
					value.WriteRune(quoted)
					i += quotedSize
				}
				if !closed {
					return nil, fmt.Errorf("unterminated quote at byte %d", start)
				}
				continue
			}
			value.WriteRune(r)
			i += size
		}
		raw := input[start:i]
		text := value.String()
		kind := queryWord
		if hadQuote && strings.HasPrefix(raw, "\"") {
			kind = queryPhrase
		}
		if kind == queryWord && !hadEscape {
			upper := strings.ToUpper(text)
			switch upper {
			case "AND", "OR", "NOT":
				kind = queryOperator
			}
		}
		tokens = append(tokens, queryToken{kind: kind, text: text, raw: raw, pos: start, quote: kind == queryPhrase})
	}
	return tokens, nil
}

func splitFieldToken(token queryToken) (string, string, bool, error) {
	if token.kind != queryWord || token.raw == "" {
		return "", "", false, nil
	}
	colon := -1
	for i := 0; i < len(token.raw); i++ {
		if token.raw[i] != ':' {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && token.raw[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			colon = i
			break
		}
	}
	if colon <= 0 || colon == len(token.raw)-1 {
		return "", "", false, nil
	}
	name := strings.ToLower(token.raw[:colon])
	if name != "tool" && name != "cwd" && name != "after" {
		return "", "", false, nil
	}
	value, err := decodeQueryFragment(token.raw[colon+1:])
	if err != nil {
		return "", "", false, fmt.Errorf("invalid %s filter: %w", name, err)
	}
	if value == "" {
		return "", "", false, nil
	}
	return name, value, true, nil
}

func decodeQueryFragment(raw string) (string, error) {
	var value strings.Builder
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		if r == utf8.RuneError && size == 1 {
			return "", fmt.Errorf("invalid UTF-8")
		}
		if r == '\\' {
			i += size
			if i >= len(raw) {
				return "", errors.New("dangling escape")
			}
			escaped, escapedSize := utf8.DecodeRuneInString(raw[i:])
			if escaped == utf8.RuneError && escapedSize == 1 {
				return "", errors.New("invalid UTF-8")
			}
			value.WriteRune(escaped)
			i += escapedSize
			continue
		}
		if r == '"' {
			i += size
			closed := false
			for i < len(raw) {
				quoted, quotedSize := utf8.DecodeRuneInString(raw[i:])
				if quoted == '\\' {
					i += quotedSize
					if i >= len(raw) {
						return "", errors.New("dangling escape")
					}
					escaped, escapedSize := utf8.DecodeRuneInString(raw[i:])
					value.WriteRune(escaped)
					i += escapedSize
					continue
				}
				if quoted == '"' {
					i += quotedSize
					closed = true
					break
				}
				value.WriteRune(quoted)
				i += quotedSize
			}
			if !closed {
				return "", errors.New("unterminated quote")
			}
			continue
		}
		value.WriteRune(r)
		i += size
	}
	return value.String(), nil
}

type queryParser struct {
	tokens []queryToken
	pos    int
}

func parseQuery(input string) (*queryExpr, queryFilters, error) {
	tokens, err := lexQuery(input)
	if err != nil {
		return nil, queryFilters{}, fmt.Errorf("invalid query: %w", err)
	}
	filters := queryFilters{}
	terms := make([]queryToken, 0, len(tokens))
	for _, token := range tokens {
		name, value, ok, err := splitFieldToken(token)
		if err != nil {
			return nil, queryFilters{}, fmt.Errorf("invalid query: %w", err)
		}
		if !ok {
			terms = append(terms, token)
			continue
		}
		switch name {
		case "tool":
			filters.tools = append(filters.tools, value)
		case "cwd":
			filters.cwds = append(filters.cwds, value)
		case "after":
			filters.afters = append(filters.afters, value)
		}
	}
	if len(terms) == 0 {
		return nil, filters, nil
	}
	parser := queryParser{tokens: terms}
	expr, err := parser.parseOr()
	if err != nil {
		return nil, queryFilters{}, fmt.Errorf("invalid query: %w", err)
	}
	if parser.pos != len(parser.tokens) {
		token := parser.tokens[parser.pos]
		return nil, queryFilters{}, fmt.Errorf("invalid query: unexpected %q at byte %d", token.text, token.pos)
	}
	if !hasPositiveQueryTerm(expr, false) {
		return nil, queryFilters{}, errors.New("invalid query: NOT requires a positive term")
	}
	return expr, filters, nil
}

func (p *queryParser) parseOr() (*queryExpr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peekKind(queryOperator) && strings.EqualFold(p.tokens[p.pos].text, "OR") {
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, errors.New("expected expression after OR")
		}
		left = &queryExpr{kind: queryOrExpr, left: left, right: right}
	}
	return left, nil
}

func (p *queryParser) parseAnd() (*queryExpr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		if p.peekKind(queryOperator) && strings.EqualFold(p.tokens[p.pos].text, "AND") {
			p.pos++
			right, err := p.parseUnary()
			if err != nil {
				return nil, errors.New("expected expression after AND")
			}
			left = &queryExpr{kind: queryAndExpr, left: left, right: right}
			continue
		}
		if p.startsExpression() {
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &queryExpr{kind: queryAndExpr, left: left, right: right}
			continue
		}
		break
	}
	return left, nil
}

func (p *queryParser) parseUnary() (*queryExpr, error) {
	if p.peekKind(queryOperator) && strings.EqualFold(p.tokens[p.pos].text, "NOT") {
		p.pos++
		if !p.startsExpression() {
			return nil, errors.New("expected expression after NOT")
		}
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &queryExpr{kind: queryNotExpr, left: child}, nil
	}
	return p.parsePrimary()
}

func (p *queryParser) parsePrimary() (*queryExpr, error) {
	if p.pos >= len(p.tokens) {
		return nil, errors.New("expected expression")
	}
	token := p.tokens[p.pos]
	switch token.kind {
	case queryWord, queryPhrase:
		p.pos++
		if token.text == "" {
			return nil, errors.New("empty term")
		}
		return &queryExpr{kind: queryAtomExpr, atom: queryAtom{value: token.text, phrase: token.kind == queryPhrase,
			key: queryAtomKey(token.text, token.kind == queryPhrase)}}, nil
	case queryLParen:
		p.pos++
		if p.peekKind(queryRParen) {
			return nil, errors.New("empty parentheses")
		}
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if !p.peekKind(queryRParen) {
			return nil, errors.New("missing closing parenthesis")
		}
		p.pos++
		return expr, nil
	case queryOperator:
		return nil, fmt.Errorf("unexpected operator %q", token.text)
	case queryRParen:
		return nil, errors.New("unexpected closing parenthesis")
	default:
		return nil, errors.New("expected expression")
	}
}

func (p *queryParser) peekKind(kind queryTokenKind) bool {
	return p.pos < len(p.tokens) && p.tokens[p.pos].kind == kind
}

func (p *queryParser) startsExpression() bool {
	if p.pos >= len(p.tokens) {
		return false
	}
	token := p.tokens[p.pos]
	if token.kind == queryWord || token.kind == queryPhrase || token.kind == queryLParen {
		return true
	}
	return token.kind == queryOperator && strings.EqualFold(token.text, "NOT")
}

func queryAtomKey(value string, phrase bool) string {
	if phrase {
		return "phrase\x00" + value
	}
	return "term\x00" + value
}

func hasPositiveQueryTerm(expr *queryExpr, negated bool) bool {
	if expr == nil {
		return false
	}
	switch expr.kind {
	case queryAtomExpr:
		return !negated
	case queryNotExpr:
		return hasPositiveQueryTerm(expr.left, !negated)
	default:
		return hasPositiveQueryTerm(expr.left, negated) || hasPositiveQueryTerm(expr.right, negated)
	}
}

func collectQueryAtoms(expr *queryExpr, negated bool, atoms *[]queryAtom) {
	if expr == nil {
		return
	}
	switch expr.kind {
	case queryAtomExpr:
		if !negated {
			*atoms = append(*atoms, expr.atom)
		}
	case queryNotExpr:
		collectQueryAtoms(expr.left, !negated, atoms)
	default:
		collectQueryAtoms(expr.left, negated, atoms)
		collectQueryAtoms(expr.right, negated, atoms)
	}
}

func cloneIDSet(values map[int64]struct{}) map[int64]struct{} {
	result := make(map[int64]struct{}, len(values))
	for id := range values {
		result[id] = struct{}{}
	}
	return result
}

func evalQuery(expr *queryExpr, universe map[int64]struct{}, atoms map[string]map[int64]struct{}) map[int64]struct{} {
	if expr == nil {
		return cloneIDSet(universe)
	}
	switch expr.kind {
	case queryAtomExpr:
		return cloneIDSet(atoms[expr.atom.key])
	case queryNotExpr:
		child := evalQuery(expr.left, universe, atoms)
		result := make(map[int64]struct{}, len(universe))
		for id := range universe {
			if _, excluded := child[id]; !excluded {
				result[id] = struct{}{}
			}
		}
		return result
	case queryAndExpr:
		left := evalQuery(expr.left, universe, atoms)
		right := evalQuery(expr.right, universe, atoms)
		if len(left) > len(right) {
			left, right = right, left
		}
		for id := range left {
			if _, ok := right[id]; !ok {
				delete(left, id)
			}
		}
		return left
	case queryOrExpr:
		left := evalQuery(expr.left, universe, atoms)
		right := evalQuery(expr.right, universe, atoms)
		for id := range right {
			left[id] = struct{}{}
		}
		return left
	default:
		return map[int64]struct{}{}
	}
}

type searchMessage struct {
	id                                                  int64
	tool, sessionID, title, cwd, sourcePath, role, text string
	created, updated, ts                                any
}

func parseAfterFilter(value string) (float64, error) {
	epoch, ok := TimestampEpoch(value)
	if !ok || epoch == nil {
		return 0, errors.New("after must be YYYY-MM-DD")
	}
	return *epoch, nil
}

func searchUniverse(db *sql.DB, tool, cwd, after string, filters queryFilters, includeSystem bool) (map[int64]searchMessage, error) {
	where := make([]string, 0, 4+len(filters.tools)+len(filters.cwds)+len(filters.afters))
	params := make([]any, 0, cap(where))
	if !includeSystem {
		where = append(where, "m.role IN ('user', 'assistant')")
	}
	if tool != "" {
		where = append(where, "s.tool = ?")
		params = append(params, tool)
	}
	if cwd != "" {
		where = append(where, `s.cwd LIKE ? ESCAPE '\'`)
		params = append(params, "%"+escapeLike(cwd)+"%")
	}
	if after != "" {
		epoch, err := parseAfterFilter(after)
		if err != nil {
			return nil, err
		}
		where = append(where, "m.ts IS NOT NULL AND m.ts >= ?")
		params = append(params, epoch)
	}
	for _, value := range filters.tools {
		where = append(where, "s.tool = ?")
		params = append(params, value)
	}
	for _, value := range filters.cwds {
		where = append(where, `s.cwd LIKE ? ESCAPE '\'`)
		params = append(params, "%"+escapeLike(value)+"%")
	}
	for _, value := range filters.afters {
		epoch, err := parseAfterFilter(value)
		if err != nil {
			return nil, err
		}
		where = append(where, "m.ts IS NOT NULL AND m.ts >= ?")
		params = append(params, epoch)
	}
	query := `SELECT m.id, s.tool, s.session_id, s.title, s.cwd, s.created, s.updated,
		s.source_path, m.ts, m.role, m.text FROM messages AS m JOIN sessions AS s ON s.id = m.session_pk`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY m.id"
	rows, err := db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]searchMessage)
	for rows.Next() {
		var row searchMessage
		if err := rows.Scan(&row.id, &row.tool, &row.sessionID, &row.title, &row.cwd, &row.created,
			&row.updated, &row.sourcePath, &row.ts, &row.role, &row.text); err != nil {
			return nil, err
		}
		result[row.id] = row
	}
	return result, rows.Err()
}

func atomMatches(text, value string) bool {
	if value == "" {
		return false
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(value))
}

func atomCandidateSets(db *sql.DB, universe map[int64]searchMessage, atoms []queryAtom) (map[string]map[int64]struct{}, error) {
	sets := make(map[string]map[int64]struct{}, len(atoms))
	for _, atom := range atoms {
		if _, exists := sets[atom.key]; exists {
			continue
		}
		set := make(map[int64]struct{})
		// Candidate retrieval uses all available indexes, but every candidate is
		// checked with a literal, case-insensitive substring match below. This
		// keeps unicode61 token boundaries, trigram's short-rune behavior, and
		// LIKE wildcard escaping equivalent from a user's perspective.
		queries := []struct {
			table string
			match string
		}{
			{table: "messages_fts", match: `"` + strings.ReplaceAll(atom.value, `"`, `""`) + `"`},
		}
		if utf8.RuneCountInString(atom.value) >= 3 {
			queries = append(queries, struct {
				table string
				match string
			}{table: "messages_tri", match: `"` + strings.ReplaceAll(atom.value, `"`, `""`) + `"`})
		}
		for _, candidate := range queries {
			rows, err := db.Query("SELECT rowid FROM "+candidate.table+" WHERE "+candidate.table+" MATCH ?", candidate.match)
			if err != nil {
				continue
			}
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				if row, ok := universe[id]; ok && atomMatches(row.text, atom.value) {
					set[id] = struct{}{}
				}
			}
			rows.Close()
		}
		// FTS5 can miss CJK strings without spaces and short/edge trigrams.
		// LIKE is escaped and only used as a candidate fallback; its result is
		// still passed through atomMatches for exact literal semantics.
		likeRows, err := db.Query(`SELECT id FROM messages WHERE text LIKE ? ESCAPE '\'`, "%"+escapeLike(atom.value)+"%")
		if err != nil {
			return nil, err
		}
		for likeRows.Next() {
			var id int64
			if err := likeRows.Scan(&id); err != nil {
				likeRows.Close()
				return nil, err
			}
			if row, ok := universe[id]; ok && atomMatches(row.text, atom.value) {
				set[id] = struct{}{}
			}
		}
		if err := likeRows.Err(); err != nil {
			likeRows.Close()
			return nil, err
		}
		likeRows.Close()
		sets[atom.key] = set
	}
	return sets, nil
}

func manualSnippetForTerms(text string, terms []string, maxChars int) string {
	compact := strings.Join(strings.Fields(text), " ")
	if len([]rune(compact)) <= maxChars {
		return compact
	}
	lowered := strings.ToLower(compact)
	position := -1
	for _, term := range terms {
		if term == "" {
			continue
		}
		found := strings.Index(lowered, strings.ToLower(term))
		if found >= 0 && (position < 0 || found < position) {
			position = found
		}
	}
	if position < 0 {
		runes := []rune(compact)
		if maxChars <= 1 {
			return string(runes[:minInt(maxChars, len(runes))])
		}
		return string(runes[:maxChars-1]) + "…"
	}
	runePosition := len([]rune(compact[:position]))
	runes := []rune(compact)
	start := maxInt(0, runePosition-maxChars/3)
	end := minInt(len(runes), start+maxChars)
	if end-start < maxChars {
		start = maxInt(0, end-maxChars)
	}
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + string([]rune(snippet)[1:])
	}
	if end < len(runes) {
		s := []rune(snippet)
		snippet = string(s[:len(s)-1]) + "…"
	}
	return string([]rune(snippet)[:minInt(maxChars, len([]rune(snippet)))])
}

// Search parses the query, evaluates boolean expressions over message IDs, and
// only then aggregates matching messages into sessions.
func Search(db *sql.DB, query, tool, cwd, after string, limit int, includeSystem bool) ([]SearchResult, error) {
	if limit <= 0 {
		return nil, errors.New("limit must be positive")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []SearchResult{}, nil
	}
	expr, filters, err := parseQuery(query)
	if err != nil {
		return nil, err
	}
	universe, err := searchUniverse(db, tool, cwd, after, filters, includeSystem)
	if err != nil {
		return nil, err
	}
	allIDs := make(map[int64]struct{}, len(universe))
	for id := range universe {
		allIDs[id] = struct{}{}
	}
	var positiveAtoms []queryAtom
	collectQueryAtoms(expr, false, &positiveAtoms)
	atoms := make([]queryAtom, 0, len(positiveAtoms))
	seenAtoms := make(map[string]bool)
	for _, atom := range positiveAtoms {
		if !seenAtoms[atom.key] {
			seenAtoms[atom.key] = true
			atoms = append(atoms, atom)
		}
	}
	// A negative child still needs a candidate set, even though it does not
	// contribute to snippets or positive-term ranking.
	var allExprAtoms func(*queryExpr)
	allExprAtoms = func(node *queryExpr) {
		if node == nil {
			return
		}
		if node.kind == queryAtomExpr {
			if !seenAtoms[node.atom.key] {
				seenAtoms[node.atom.key] = true
				atoms = append(atoms, node.atom)
			}
			return
		}
		allExprAtoms(node.left)
		allExprAtoms(node.right)
	}
	allExprAtoms(expr)
	candidateSets, err := atomCandidateSets(db, universe, atoms)
	if err != nil {
		return nil, err
	}
	matchedIDs := evalQuery(expr, allIDs, candidateSets)
	if len(matchedIDs) == 0 {
		return []SearchResult{}, nil
	}
	orderedIDs := make([]int64, 0, len(matchedIDs))
	for id := range matchedIDs {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Slice(orderedIDs, func(i, j int) bool { return orderedIDs[i] < orderedIDs[j] })

	type searchGroup struct {
		result SearchResult
		ids    []int64
	}
	groups := make(map[string]*searchGroup)
	order := make([]string, 0)
	for _, id := range orderedIDs {
		row := universe[id]
		key := row.tool + "\x00" + row.sessionID
		group := groups[key]
		if group == nil {
			group = &searchGroup{result: SearchResult{Tool: row.tool, SessionID: row.sessionID,
				Title: row.title, CWD: row.cwd, Created: FormatTimestamp(row.created), Updated: FormatTimestamp(row.updated),
				Snippets: []string{}, SourcePaths: []string{}, matchedTerms: map[string]bool{},
				createdEpoch: sqlFloat(row.created), updatedEpoch: sqlFloat(row.updated)}}
			groups[key] = group
			order = append(order, key)
		}
		group.result.MessageCount++
		if len(group.ids) < 6 {
			group.ids = append(group.ids, id)
		}
		group.result.createdEpoch = mergeMin(group.result.createdEpoch, sqlFloat(row.created))
		group.result.updatedEpoch = mergeMax(group.result.updatedEpoch, sqlFloat(row.updated))
		if group.result.Title == "" && row.title != "" {
			group.result.Title = row.title
		}
		if group.result.CWD == "" && row.cwd != "" {
			group.result.CWD = row.cwd
		}
		group.result.Created = FormatTimestamp(timestampValue(group.result.createdEpoch))
		group.result.Updated = FormatTimestamp(timestampValue(group.result.updatedEpoch))
		if !containsString(group.result.SourcePaths, row.sourcePath) {
			group.result.SourcePaths = append(group.result.SourcePaths, row.sourcePath)
		}
		for _, atom := range positiveAtoms {
			if set := candidateSets[atom.key]; set != nil {
				if _, ok := set[id]; ok {
					group.result.matchedTerms[atom.key] = true
				}
			}
		}
	}
	results := make([]SearchResult, 0, len(order))
	for _, key := range order {
		results = append(results, groups[key].result)
	}
	sort.SliceStable(results, func(i, j int) bool {
		left, right := results[i], results[j]
		if len(left.matchedTerms) != len(right.matchedTerms) {
			return len(left.matchedTerms) > len(right.matchedTerms)
		}
		if left.MessageCount != right.MessageCount {
			return left.MessageCount > right.MessageCount
		}
		leftUpdated, rightUpdated := left.updatedEpoch, right.updatedEpoch
		if leftUpdated == nil || rightUpdated == nil {
			if leftUpdated != rightUpdated {
				return leftUpdated != nil
			}
		} else if *leftUpdated != *rightUpdated {
			return *leftUpdated > *rightUpdated
		}
		if left.Tool != right.Tool {
			return left.Tool < right.Tool
		}
		return left.SessionID < right.SessionID
	})
	if len(results) > limit {
		results = results[:limit]
	}
	termsForSnippet := make([]string, 0, len(positiveAtoms))
	seenSnippetTerms := make(map[string]bool)
	for _, atom := range positiveAtoms {
		if !seenSnippetTerms[atom.value] {
			seenSnippetTerms[atom.value] = true
			termsForSnippet = append(termsForSnippet, atom.value)
		}
	}
	for i := range results {
		group := groups[results[i].Tool+"\x00"+results[i].SessionID]
		sort.Slice(group.ids, func(a, b int) bool { return group.ids[a] < group.ids[b] })
		for _, id := range group.ids {
			row := universe[id]
			snippet := manualSnippetForTerms(row.text, termsForSnippet, 200)
			if snippet != "" && !containsString(results[i].Snippets, snippet) {
				results[i].Snippets = append(results[i].Snippets, snippet)
			}
			if len(results[i].Snippets) >= 3 {
				break
			}
		}
		results[i].matchedTerms = nil
		results[i].updatedEpoch = nil
	}
	return results, nil
}

// Kept for tests and callers that want to inspect the parser without opening
// SQLite. It intentionally returns a nil expression for a field-only query.
func parseSearchQuery(query string) (*queryExpr, queryFilters, error) {
	return parseQuery(query)
}

// queryDebugString is intentionally small and stable for parser tests and
// diagnostics; callers should use Search for actual result retrieval.
func queryDebugString(query string) (string, error) {
	expr, filters, err := parseQuery(query)
	if err != nil {
		return "", err
	}
	var render func(*queryExpr) string
	render = func(node *queryExpr) string {
		if node == nil {
			return ""
		}
		switch node.kind {
		case queryAtomExpr:
			if node.atom.phrase {
				return `"` + node.atom.value + `"`
			}
			return node.atom.value
		case queryNotExpr:
			return "NOT " + render(node.left)
		case queryAndExpr:
			return "(" + render(node.left) + " AND " + render(node.right) + ")"
		case queryOrExpr:
			return "(" + render(node.left) + " OR " + render(node.right) + ")"
		default:
			return ""
		}
	}
	parts := make([]string, 0, len(filters.tools)+len(filters.cwds)+len(filters.afters)+1)
	for _, value := range filters.tools {
		parts = append(parts, "tool:"+value)
	}
	for _, value := range filters.cwds {
		parts = append(parts, "cwd:"+value)
	}
	for _, value := range filters.afters {
		parts = append(parts, "after:"+value)
	}
	if value := render(expr); value != "" {
		parts = append(parts, value)
	}
	return strings.Join(parts, " "), nil
}
