import { expect, test } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { workspaceFixture } from "./fixture";

const fixtures = process.env.PREVIEW_FIXTURES;
test.skip(!fixtures, "Requires generated Office fixtures");
for (const locale of ["en-US", "zh-CN"])
  for (const theme of ["light", "dark"] as const)
    for (const width of [390, 1600]) {
      test(`presentation keyboard and frame lifecycle ${locale} ${theme} ${width}`, async ({
        browser,
      }) => {
        const context = await browser.newContext({
          locale,
          colorScheme: theme,
          reducedMotion: "reduce",
          viewport: { width, height: 1000 },
        });
        const page = await context.newPage();
        try {
          const f = await workspaceFixture(page);
          f.messages.analysis[1].content = "[Document](/workspace/charts.pptx)";
          await page.route("**/files/content?*", (route) =>
            route.fulfill({
              body: readFileSync(resolve(fixtures!, "charts.pptx")),
            }),
          );
          const errors: string[] = [];
          page.on("pageerror", (error) => errors.push(error.message));
          const cdp = await context.newCDPSession(page);
          if (width === 390)
            await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });
          await page.goto("/?project=battery&session=analysis");
          await page
            .getByRole("link", { name: "Document", exact: true })
            .click();
          await expect(page.locator(".office-preview")).toHaveAttribute(
            "data-preview-phase",
            "ready",
            { timeout: 15000 },
          );
          const chinese = locale === "zh-CN";
          if (width === 390)
            await page
              .getByLabel(chinese ? "切换文件树" : "Toggle file tree", {
                exact: true,
              })
              .click();
          const frame = page.frameLocator(".office-frame");
          const next = frame.getByRole("button", {
            name: chinese ? "下一页" : "Next slide",
            exact: true,
          });
          const previous = frame.getByRole("button", {
            name: chinese ? "上一页" : "Previous slide",
            exact: true,
          });
          await expect(
            frame.getByRole("navigation", {
              name: chinese ? "幻灯片" : "Slides",
              exact: true,
            }),
          ).toBeVisible();
          await expect(previous).toBeDisabled();
          await next.focus();
          await page.keyboard.press("Space");
          await expect(frame.locator(".office-frame-content")).toContainText(
            "charts slide 1",
          );
          await expect(frame.locator(".chart-node svg")).toHaveCount(1);
          await expect(next).toBeDisabled();
          await previous.focus();
          await page.keyboard.press("Enter");
          await expect(frame.locator(".office-frame-content")).toContainText(
            "charts slide 0",
          );
          await expect
            .poll(() =>
              frame.locator(".chart-node svg").evaluate(
                (svg) =>
                  Array.from(svg.querySelectorAll("path")).filter((path) => {
                    const fill = path.getAttribute("fill") || "none";
                    const box = path.getBBox();
                    return (
                      ![
                        "none",
                        "transparent",
                        "#fff",
                        "#ffffff",
                        "#000",
                        "#000000",
                        "white",
                        "black",
                      ].includes(fill) &&
                      Number(path.getAttribute("fill-opacity") ?? 1) > 0 &&
                      box.width > 5 &&
                      box.height > 5
                    );
                  }).length,
              ),
            )
            .toBeGreaterThanOrEqual(2);
          await expect
            .poll(() =>
              frame
                .locator(".pptx-preview-slide-wrapper-0")
                .evaluate(
                  (slide) =>
                    slide.getBoundingClientRect().top >=
                    slide.parentElement!.getBoundingClientRect().top - 1,
                ),
            )
            .toBe(true);
          await page
            .getByLabel(chinese ? "进入全屏" : "Enter Full Screen", {
              exact: true,
            })
            .click();
          await next.focus();
          await page.keyboard.press("Escape");
          await expect(
            page.getByLabel(chinese ? "进入全屏" : "Enter Full Screen", {
              exact: true,
            }),
          ).toBeFocused();
          const changed = theme === "light" ? "dark" : "light";
          await page.evaluate((theme) => {
            localStorage.setItem("project-theme", theme);
            document.documentElement.dataset.theme = theme;
          }, changed);
          await expect(frame.locator("html")).toHaveAttribute(
            "data-theme",
            changed,
          );
          await page.setViewportSize({
            width: width === 390 ? 430 : 1400,
            height: 900,
          });
          await expect
            .poll(() =>
              frame
                .locator(".pptx-preview-wrapper")
                .evaluate(
                  (element) =>
                    element.getBoundingClientRect().width <=
                    document.documentElement.clientWidth + 1,
                ),
            )
            .toBe(true);
          await expect(page.locator(".office-frame")).toHaveCount(1);
          writeFileSync(
            test.info().outputPath("chart.svg"),
            await frame
              .locator(".chart-node svg")
              .evaluate((svg) => svg.outerHTML),
          );
          await page.screenshot({
            path: test.info().outputPath("presentation.png"),
            fullPage: true,
          });
          expect(errors).toEqual([]);
          await cdp.detach();
        } finally {
          await context.close();
        }
      });
    }
