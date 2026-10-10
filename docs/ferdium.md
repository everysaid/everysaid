# Everysaid in Ferdium (and Franz)

Ferdium gathers web apps in one window, each as a "service" made from a recipe. Everysaid has a
recipe of its own, made by the binary itself (`internal/ferdium`), so that the number of unread
chats shows on its icon in Ferdium's sidebar. The plain "Custom Website" service does not show it:
it only opens the page, and Ferdium shows a number only when a recipe hands it over
(`Ferdium.setBadge`).

## What the recipe does

- It opens the app at the server's address (`[server] origin`, the one passkeys are tied to: use
  the same address as in a browser), offered as the service's own ("hosted") one, ready to save;
  another can be given under "Self hosted".
- It reads the number of unread chats from the page's title, `(3) Everysaid` (the app writes it
  there for every browser tab too), and hands it to Ferdium as it changes (Ferdium.loop).
- Notifications need nothing of it. Ferdium is built on Electron, which has no push service, so the
  page shows them itself while the service is loaded (in the background too): turn on Settings →
  Notifications in the app inside Ferdium, once. Plugins' warnings come the same way.

Files: `package.json` (id `everysaid`, the address as `serviceURL`), `index.js`, `webview.js` (the
number), `icon.svg` (the app's icon). The recipe's `version` (in `internal/ferdium`) goes up when its
files change.

## Installing

On the machine where Ferdium runs, if it is the server's too:

```sh
everysaid ferdium install            # into each of Ferdium, Ferdi, Franz found here
everysaid ferdium install --url https://chat.example --dir PATH/recipes/dev
```

On another machine: `everysaid ferdium zip everysaid-ferdium.zip` on the server, then unpack the
zip into the app's folder of development recipes, which then holds `everysaid/package.json`:

| System | Folder |
|---|---|
| Linux | `~/.config/Ferdium/recipes/dev/` |
| macOS | `~/Library/Application Support/Ferdium/recipes/dev/` |
| Windows | `%APPDATA%\Ferdium\recipes\dev\` |

(For Franz or Ferdi, the same with `Franz` or `Ferdi` in place of `Ferdium`.)

Then restart Ferdium, and add the service: Add a service → Development → Everysaid. A service
already made with "Custom Website" can be removed once the new one works.

To check the number: Settings → Notifications → Test notification puts 1 on the icon for ten
seconds, whatever is unread.

## Where else the recipe works

The recipe format is Franz's, which Ferdi and then Ferdium took over with the same API: the recipe
works in **Ferdium**, **Franz** (5) and **Ferdi** (no longer maintained). Other apps of the kind
have their own ways, not these recipes (Rambox, Wavebox); a browser's installed web app needs none:
it shows the number on its icon by itself (the browser's badge API).
