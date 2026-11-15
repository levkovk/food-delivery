"""Снимки веб-интерфейсов в Chromium (Playwright).

    python demo/browser.py panel   <адрес> <каталог> <пароль оператора> <пароль администратора>
    python demo/browser.py grafana <адрес> <файл.png> <пароль>
    python demo/browser.py page    <адрес> <файл.png>
"""

import asyncio
import sys
from pathlib import Path

from playwright.async_api import async_playwright


async def page_for(browser, width=1440, height=900):
    ctx = await browser.new_context(viewport={"width": width, "height": height},
                                    device_scale_factor=1.5, locale="ru-RU")
    return await ctx.new_page()


async def wait_login(page, url):
    for _ in range(30):
        try:
            await page.goto(url, wait_until="domcontentloaded", timeout=15000)
            await page.wait_for_selector("#login-username", timeout=5000)
            return
        except Exception:  # noqa: BLE001 - кластер может ещё подниматься
            await page.wait_for_timeout(4000)
    raise RuntimeError("форма входа не появилась")


async def sign_in(page, user, password):
    await page.fill("#login-username", user)
    await page.fill("#login-password", password)
    await page.click("#login-form button[type=submit]")


async def panel(browser, url, out: Path, operator_pw, admin_pw):
    page = await page_for(browser)
    await wait_login(page, url)
    await sign_in(page, "operator", "not-the-password")
    await page.wait_for_timeout(700)
    await page.screenshot(path=str(out / "login.png"))

    await sign_in(page, "operator", operator_pw)
    await page.wait_for_selector(".ticket")
    await page.wait_for_timeout(2500)
    await page.screenshot(path=str(out / "board.png"))

    await page.click(".column:nth-child(6) .ticket")
    await page.wait_for_selector("#details .history li")
    await page.wait_for_timeout(500)
    await page.screenshot(path=str(out / "order-details.png"))
    await page.click("#details-close")

    await page.click("button[data-tab=couriers]")
    await page.wait_for_selector("#couriers tr td")
    await page.wait_for_timeout(500)
    await page.screenshot(path=str(out / "couriers.png"))

    await page.click("button[data-tab=feed]")
    await page.wait_for_timeout(9000)
    await page.screenshot(path=str(out / "feed.png"))

    await page.click("#logout")
    await sign_in(page, "admin", admin_pw)
    await page.wait_for_selector(".ticket")
    await page.click("button[data-tab=restaurants]")
    await page.wait_for_selector("#menu li")
    await page.wait_for_timeout(500)
    await page.screenshot(path=str(out / "restaurants.png"))
    await page.click("button[data-tab=payments]")
    await page.wait_for_selector("#payments tr td")
    await page.wait_for_timeout(500)
    await page.screenshot(path=str(out / "payments.png"))


async def grafana(browser, url, target, password):
    page = await page_for(browser, 1440, 1400)
    await page.goto(f"{url}/login", wait_until="networkidle")
    await page.fill("input[name=user]", "admin")
    await page.fill("input[name=password]", password)
    await page.click("button[type=submit]")
    await page.wait_for_timeout(3000)
    await page.goto(f"{url}/d/food-delivery/?orgId=1&from=now-20m&to=now&kiosk", wait_until="networkidle")
    await page.wait_for_timeout(8000)
    await page.screenshot(path=str(target))


async def single(browser, url, target):
    page = await page_for(browser, 1440, 1000)
    await page.goto(url, wait_until="networkidle")
    await page.wait_for_timeout(3000)
    await page.screenshot(path=str(target))


async def main():
    mode, args = sys.argv[1], sys.argv[2:]
    async with async_playwright() as p:
        browser = await p.chromium.launch()
        if mode == "panel":
            await panel(browser, args[0], Path(args[1]), args[2], args[3])
        elif mode == "grafana":
            await grafana(browser, args[0], args[1], args[2])
        elif mode == "page":
            await single(browser, args[0], args[1])
        else:
            raise SystemExit(f"unknown mode {mode}")
        await browser.close()


if __name__ == "__main__":
    asyncio.run(main())
