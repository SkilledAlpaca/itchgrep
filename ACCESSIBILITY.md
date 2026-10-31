# Accessibility statement

This statement describes the accessibility of [itchgrep.com](https://itchgrep.com/),
which this repository builds and serves.

## Commitment

itchgrep is meant to be usable by everyone, including people who use
keyboards, screen readers, magnification, or high-contrast settings. The
project treats an accessibility barrier as a bug and fixes it like one.

## Conformance status

itchgrep conforms to the
[Web Content Accessibility Guidelines (WCAG) 2.2](https://www.w3.org/TR/WCAG22/)
at Level AA. "Conforms" means every Level A and Level AA success criterion is
met, or doesn't apply to the content, on every page in scope.

The site was evaluated on 2026-10-31. It doesn't claim Level AAA; see
[Known limitations](#known-limitations).

## Scope

The statement covers every page of itchgrep.com that this repository's web
server renders: search results, tag pages, the new-assets list, saved assets,
the about and stats pages, and the error pages.

It excludes third-party content that the site displays but doesn't author:
asset titles, descriptions, thumbnails, and author names, which come from
itch.io unchanged. Their alternative text, language, and wording are outside
the site's control. The site does hide decorative thumbnails from assistive
technology, because each card's title link already names the asset.

## Evaluation method

The evaluation compared the live site, criterion by criterion, with every WCAG
2.2 Level A and Level AA success criterion.

Automated checks:

- axe-core, run with the `wcag2a`, `wcag2aa`, `wcag21a`, `wcag21aa`, and
  `wcag22aa` rule sets, across every page type and in both the light and dark
  themes at 1280 px and 320 px viewport widths.

Manual checks:

- Keyboard-only walkthroughs of every page and every interactive flow,
  including focus order, focus visibility, and focus after each in-page update.
- Accessibility-tree inspection of dynamic updates: the live status region,
  the save toggle, and the tag disclosure.
- Contrast measurement of text and non-text elements in every state, in both
  themes.
- Reflow at 320 px, text at 200% size, text-spacing overrides, and target
  size.
- Code review of the templates, stylesheet, and script for criteria that
  scripts can't test, such as timing, shortcuts, pointer handling, and motion.

The evaluation re-ran after the fixes for the findings it raised.

## Known limitations

- No testing with real screen readers yet. Announcements were inferred from
  the Chromium accessibility tree and ARIA semantics, not heard.
- Testing used Chromium only. Firefox and Safari weren't tested.
- Zoom was emulated with viewport sizes and a doubled root font size, not real
  browser zoom or operating-system text scaling.
- Forced-colors mode was reviewed in the stylesheet but not rendered.
- No testing with speech input, switch access, or a real touch device.
- Some supplementary details are available only in tooltips (`title`
  attributes) and aren't reachable by keyboard or touch: the exact index
  timestamp, exact coverage counts, and price-conversion detail.
- A few small touch targets, such as the author links on result cards, meet
  the Level AA 24 px minimum only through the spacing exception.
- Long asset titles and descriptions on result cards are clamped to two lines.
  The full title is in each link's accessible name.

### Level AAA

Level AAA isn't claimed. Notable Level AAA criteria that the site doesn't meet:

- 1.4.6 Contrast (Enhanced): the secondary "faint" text, such as index age,
  facet counts, descriptions, and placeholders, meets 4.5:1 but not 7:1.
- 2.5.5 Target Size (Enhanced): dense tag chips are smaller than 44 px.
- 2.4.12 Focus Not Obscured (Enhanced): at very small viewport heights, the
  sticky search bar can partly cover a focused result link.
- 2.4.9 Link Purpose (Link Only): facet links and card tag chips need their
  surrounding context to be understood.
- 3.1.3 Unusual Words, 3.1.4 Abbreviations, and 3.3.5 Help.

## Compatibility

The site is designed for current versions of evergreen browsers. Searching and
filtering work without JavaScript, because every control is a plain link or
form. The saved-assets page stores its list in your browser and needs
JavaScript; it says so when scripting is off.

## Technical specifications

The site relies on these technologies to conform with WCAG:

- HTML
- CSS
- WAI-ARIA
- JavaScript, which is optional except on the saved-assets page

## Feedback

If you hit an accessibility barrier, or you think this statement is wrong,
[open an issue](https://github.com/SkilledAlpaca/itchgrep/issues) on GitHub.
Include:

- The page address and what you were trying to do.
- What happened and what you expected.
- Your browser, operating system, and any assistive technology.

Accessibility reports are triaged ahead of feature requests. This is a
one-person project, so replies can take a few days.

## How this is maintained

Accessibility checks are part of reviewing every change that touches the user
interface. The site is re-evaluated after significant UI changes, and this
statement is updated when the result changes.
