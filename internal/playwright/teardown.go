package playwright

// Teardown closes the context (and with it every tab), then the browser, and
// finally stops Playwright. It is best-effort: every step runs even if an
// earlier one fails, and the first error encountered is returned.
func Teardown() error {
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if browserContext != nil {
		record(browserContext.Close())
	}
	if browser != nil {
		record(browser.Close())
	}
	if pw != nil {
		record(pw.Stop())
	}

	return firstErr
}
