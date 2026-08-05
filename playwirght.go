package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/mxschmitt/playwright-go"
)

type FacebookPost struct {
	URL     string   `json:"url"`
	Author  string   `json:"author,omitempty"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

func mainA() {
	headless := flag.Bool(
		"headless",
		true,
		"run browser in headless mode",
	)

	flag.Parse()

	if flag.NArg() == 0 {
		log.Fatal(`usage: go run . "https://www.facebook.com/..."`)
	}

	postURL := flag.Arg(0)

	if err := validateFacebookURL(postURL); err != nil {
		log.Fatal(err)
	}

	post, err := scrapeFacebookPost(postURL, *headless)
	if err != nil {
		log.Fatal(err)
	}

	output, err := json.MarshalIndent(post, "", "  ")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(output))
}

func scrapeFacebookPost(
	postURL string,
	headless bool,
) (*FacebookPost, error) {

	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf(
			"could not start playwright: %w",
			err,
		)
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch(
		playwright.BrowserTypeLaunchOptions{
			Headless: playwright.Bool(headless),
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"could not launch chromium: %w",
			err,
		)
	}
	defer browser.Close()

	context, err := browser.NewContext(
		playwright.BrowserNewContextOptions{
			Locale: playwright.String("vi-VN"),
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"could not create browser context: %w",
			err,
		)
	}
	defer context.Close()

	page, err := context.NewPage()
	if err != nil {
		return nil, fmt.Errorf(
			"could not create page: %w",
			err,
		)
	}

	_, err = page.Goto(
		postURL,
		playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(60_000),
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"could not open facebook post: %w",
			err,
		)
	}

	// Facebook thường render post dưới role="article".
	article := page.Locator(`[role="article"]`).First()

	err = article.WaitFor(
		playwright.LocatorWaitForOptions{
			Timeout: playwright.Float(20_000),
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"could not find facebook post; " +
				"post may not be public or Facebook DOM changed",
		)
	}

	// Click "Xem thêm" nếu post bị collapse.
	expandPost(article)

	result, err := article.Evaluate(`
		(el) => {
			const clean = value => {
				if (!value) {
					return "";
				}

				return value
					.replace(/\u00a0/g, " ")
					.replace(/\n{3,}/g, "\n\n")
					.trim();
			};

			/*
				Ưu tiên các selector semantic mà
				Facebook dùng cho nội dung post.
			*/
			const message =
				el.querySelector(
					'[data-ad-preview="message"]'
				) ||
				el.querySelector(
					'[data-ad-comet-preview="message"]'
				) ||
				el.querySelector(
					'[data-testid="post_message"]'
				);

			let content = "";

			if (message) {
				content = clean(message.innerText);
			}

			/*
				Fallback nếu Facebook thay đổi selector.

				Tìm block text lớn nhất trong article.
			*/
			if (!content) {
				const candidates = Array.from(
					el.querySelectorAll(
						'div[dir="auto"]'
					)
				)
				.map(node => clean(node.innerText))
				.filter(value => value.length > 0)
				.sort(
					(a, b) =>
						b.length - a.length
				);

				if (candidates.length > 0) {
					content = candidates[0];
				}
			}

			/*
				Tìm author.

				Đây chỉ là heuristic vì Facebook
				không có selector author ổn định.
			*/
			let author = "";

			const links = Array.from(
				el.querySelectorAll(
					'h2 a, h3 a, strong a'
				)
			);

			for (const link of links) {
				const value = clean(
					link.innerText
				);

				if (
					value &&
					value.length < 100
				) {
					author = value;
					break;
				}
			}

			/*
				Chỉ lấy ảnh đủ lớn để tránh:
				- avatar
				- icon
				- emoji
			*/
			const images = Array.from(
				el.querySelectorAll('img')
			)
			.filter(img => {
				const rect =
					img.getBoundingClientRect();

				return (
					rect.width >= 200 ||
					rect.height >= 200
				);
			})
			.map(
				img =>
					img.currentSrc ||
					img.src
			)
			.filter(Boolean);

			return {
				author,
				content,
				images: [...new Set(images)]
			};
		}
	`, nil)
	if err != nil {
		return nil, fmt.Errorf(
			"could not extract post: %w",
			err,
		)
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}

	var extracted struct {
		Author  string   `json:"author"`
		Content string   `json:"content"`
		Images  []string `json:"images"`
	}

	if err := json.Unmarshal(
		data,
		&extracted,
	); err != nil {
		return nil, err
	}

	return &FacebookPost{
		URL:     page.URL(),
		Author:  extracted.Author,
		Content: extracted.Content,
		Images:  extracted.Images,
	}, nil
}

func expandPost(article playwright.Locator) {
	selectors := []string{
		`div[role="button"]:has-text("Xem thêm")`,
		`span[role="button"]:has-text("Xem thêm")`,
		`div[role="button"]:has-text("See more")`,
		`span[role="button"]:has-text("See more")`,
	}

	for _, selector := range selectors {
		button := article.Locator(selector).First()

		count, err := button.Count()
		if err != nil || count == 0 {
			continue
		}

		_ = button.Click(
			playwright.LocatorClickOptions{
				Timeout: playwright.Float(2_000),
			},
		)

		return
	}
}

func validateFacebookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf(
			"invalid URL: %w",
			err,
		)
	}

	host := strings.ToLower(
		u.Hostname(),
	)

	switch host {
	case "facebook.com",
		"www.facebook.com",
		"m.facebook.com":
		return nil

	default:
		return fmt.Errorf(
			"URL must belong to facebook.com",
		)
	}
}
