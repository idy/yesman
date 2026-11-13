# Project website

The public project website is [yesman.idy.dev](https://yesman.idy.dev/). Its static source is in `site/`, separate from the desktop application's embedded `web/` assets. English and Simplified Chinese content follows the implementation status and platform limitations in `README.md`.

## Preview

Serve `site/` with any static HTTP server. No application process, pi session, API key, or frontend build is needed. The website vendors the complete idy.dev Site SDK 0.2.0 distribution in `site/shared/sdk/`, including its fonts and third-party licenses. The SDK supplies language and theme preferences, command copying, and validated GitHub release lookups.

`site/page.js` adds the website's translations and release-package matching. Essential navigation and the GitHub Releases fallback work without JavaScript or when GitHub is unavailable. Downloads are discovered from uploaded assets in the latest published release; version numbers and package URLs are not guessed.

## Design and publishing

The website inherits the Component Datasheet system from [idy/idy](https://github.com/idy/idy/blob/main/DESIGN.md): Archivo, a blue masthead, light/dark sheets, 2px corners, and ruled sections. The desktop app continues to use Magpie.

`.github/workflows/pages.yml` checks the website on pull requests and deploys `site/` on relevant pushes to `main` or manual dispatch from `main`. Deployment runs share one concurrency group and do not cancel a deployment already in progress. Configure GitHub Pages to use GitHub Actions and bind `yesman.idy.dev` before adding its DNS CNAME. Name.com DNS is managed in the `idy/letsolo` Terraform integration; the website's `CNAME` file does not create DNS records.

To update the shared website kit, copy the complete built `packages/site-sdk/dist/` from the intended idy/idy release into `site/shared/sdk/`, retaining `assets/` and licenses, then verify both themes, both languages, narrow screens, command copying, and unavailable release metadata.
