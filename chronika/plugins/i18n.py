"""The plugins' words in other languages. Plugins are written in English; a manifest or a status
is given in the user's language through tr(). A text without a translation stays in English."""

EL = {
    "Going back to asking takes it out of the keyring": "Αν γυρίσεις στο να ζητείται, σβήνεται από το keyring",
    # iphone-sync.py, as its lines show in the app
    "There is no complete backup yet (its Manifest.plist is missing).":
        "Δεν υπάρχει ακόμα ολοκληρωμένο backup (λείπει το Manifest.plist του).",
    "Wrong backup password.": "Λάθος κωδικός backup.",
    "The iPhone may ask for its passcode: type it there.": "Το iPhone μπορεί να ζητήσει τον κωδικό του: γράψ' τον εκεί.",
    "The backup failed: the iPhone is locked. Unlock it, and type its passcode there when it asks.":
        "Το backup απέτυχε: το iPhone είναι κλειδωμένο. Ξεκλείδωσέ το και γράψε τον κωδικό του εκεί όταν τον ζητήσει.",
    "The backup failed: no iPhone found. Connect it with a cable, unlock it and tap Trust.":
        "Το backup απέτυχε: δεν βρέθηκε iPhone. Σύνδεσέ το με καλώδιο, ξεκλείδωσέ το και πάτα «Εμπιστεύομαι».",
    "The backup failed: idevicebackup2 did not finish (see the whole log).":
        "Το backup απέτυχε: το idevicebackup2 δεν ολοκληρώθηκε (δες το πλήρες log).",
    "Backup folder": "Φάκελος backup",
    "Where the encrypted backup is kept (a folder for each phone inside)":
        "Πού κρατιέται το κρυπτογραφημένο backup (ένας φάκελος για κάθε κινητό μέσα του)",
    "The backup password": "Ο κωδικός του backup",
    "Asked for at each import, kept nowhere": "Ζητείται σε κάθε εισαγωγή, δεν φυλάσσεται πουθενά",
    "Kept in the system's keyring": "Φυλάσσεται στο keyring του συστήματος",
    "Last backup": "Τελευταίο backup",
    "none yet": "κανένα ακόμα",
    "Size": "Μέγεθος",
    "The backup password is needed": "Χρειάζεται ο κωδικός του backup",
    "Demo (sends into the demo archive)": "Demo (στέλνει στο αρχείο του demo)",
    "Invented: what is sent is only written into the demo archive.": "Επινοημένη: ό,τι στέλνεται γράφεται μόνο στο αρχείο του demo.",
    "Serial": "Σειριακός αριθμός",
    "e.g. https://cloud.example.org/remote.php/dav/addressbooks/users/NAME/contacts/":
        "π.χ. https://cloud.example.org/remote.php/dav/addressbooks/users/NAME/contacts/",
    # the app's words said by the server (logs, statuses, notifications, errors of plugins)
    "unknown plugin": "άγνωστο plugin",
    "import": "εισαγωγή",
    "error: {e}": "σφάλμα: {e}",
    "live: error {e}; again in {pause}s": "ζωντανή σύνδεση: σφάλμα {e}· ξανά σε {pause} δ",
    "caught up: {n} new messages in {chats} chats": "ενημέρωση: {n} νέα μηνύματα σε {chats} συνομιλίες",
    "caught up: nothing new": "ενημέρωση: τίποτα νέο",
    "connected to Telegram": "συνδέθηκε στο Telegram",
    "new message in {chat}": "νέο μήνυμα στο {chat}",
    "new messages: {m}, new calls: {c}": "νέα μηνύματα: {m}, νέες κλήσεις: {c}",
    "== {label}": "== {label}",
    "calls": "κλήσεις",
    "WhatsApp and Viber calls": "κλήσεις WhatsApp και Viber",
    "files": "αρχεία",
    "WhatsApp (bridge)": "WhatsApp (γέφυρα)",
    "app and carrier calls": "κλήσεις εφαρμογών και παρόχου",
    "contacts: {n}, addresses found in the archive: {linked}": "επαφές: {n}, διευθύνσεις που βρέθηκαν στο αρχείο: {linked}",
    "📷 Photo": "📷 Φωτογραφία",
    "🎬 Video": "🎬 Βίντεο",
    "🎤 Voice message": "🎤 Φωνητικό μήνυμα",
    "📎 File": "📎 Αρχείο",
    "Sticker": "Αυτοκόλλητο",
    "📍 Location": "📍 Τοποθεσία",
    "👤 Contact": "👤 Επαφή",
    "New message": "Νέο μήνυμα",
    "Test notification": "Δοκιμαστική ειδοποίηση",
    "Notes": "Σημειώσεις",
    "Not signed in to Telegram yet (scripts/telegram-sync.py --save-credentials, --login)":
        "Δεν έχει γίνει ακόμα σύνδεση στο Telegram (scripts/telegram-sync.py --save-credentials, --login)",
    "The Telegram sign-in has expired: scripts/telegram-sync.py --login":
        "Η σύνδεση στο Telegram έληξε: scripts/telegram-sync.py --login",
    "Sending is off in this source's settings": "Η αποστολή είναι κλειστή στις ρυθμίσεις αυτής της πηγής",
    "Unknown person to mention": "Άγνωστο πρόσωπο για αναφορά με @",
    "read receipts: {e}": "αποδείξεις ανάγνωσης: {e}",
    "Send read receipts": "Αποστολή αποδείξεων ανάγνωσης",
    "When a chat is opened here, the others see it read, and it is read on the phone too":
        "Όταν ανοίγει μια συνομιλία εδώ, οι άλλοι τη βλέπουν διαβασμένη, και διαβάζεται και στο κινητό",
    "The bridge does not offer read receipts (/api/read)": "Η γέφυρα δεν προσφέρει αποδείξεις ανάγνωσης (/api/read)",
    "The bridge does not offer sending (/api/send)": "Η γέφυρα δεν προσφέρει αποστολή (/api/send)",
    "Sending failed": "Η αποστολή απέτυχε",
    # names
    "iPhone (encrypted backup)": "iPhone (κρυπτογραφημένο backup)",
    "Android (adb)": "Android (adb)",
    "Viber Desktop export": "Εξαγωγή Viber Desktop",
    "WhatsApp (live bridge)": "WhatsApp (ζωντανή γέφυρα)",
    "Carrier missed-call notices": "Ειδοποιήσεις αναπάντητων του παρόχου",
    "Adium and Pidgin logs": "Ιστορικό Adium και Pidgin",
    "The logs of the old multi-protocol messengers, Adium (macOS) and Pidgin or Gaim: MSN, ICQ, AIM, "
    "Yahoo, Jabber and Google Talk, Skype, IRC, Facebook chat. Read from their folders as they are.":
        "Το ιστορικό των παλιών προγραμμάτων πολλαπλών πρωτοκόλλων, Adium (macOS) και Pidgin ή Gaim: MSN, ICQ, AIM, "
        "Yahoo, Jabber και Google Talk, Skype, IRC, Facebook chat. Διαβάζεται από τους φακέλους τους όπως είναι.",
    "Adium's folder (Adium 2.0, Users/Default or Logs), or Pidgin's .purple folder, unpacked":
        "Ο φάκελος του Adium (Adium 2.0, Users/Default ή Logs) ή ο φάκελος .purple του Pidgin, αποσυμπιεσμένοι",
    "Adium folder": "Φάκελος Adium",
    "Adium 2.0, its Users/Default, or its Logs folder": "Adium 2.0, το Users/Default του, ή ο φάκελος Logs του",
    "Pidgin folder": "Φάκελος Pidgin",
    "The .purple folder (with blist.xml and accounts.xml), or its logs folder":
        "Ο φάκελος .purple (με blist.xml και accounts.xml), ή ο φάκελος logs του",
    "missing: a folder of Adium or of Pidgin": "λείπει: ένας φάκελος του Adium ή του Pidgin",
    "Folder": "Φάκελος",
    "Address book": "Επαφές (βιβλίο διευθύνσεων)",
    "address book copy": "αντίγραφο των επαφών",
    "chat name": "όνομα συνομιλίας",
    "chosen by them": "όνομα που διάλεξε ο ίδιος",
    "Phone": "Τηλέφωνο",
    "Contacts file (.vcf)": "Αρχείο επαφών (.vcf)",
    "Address book (CardDAV)": "Βιβλίο διευθύνσεων (CardDAV)",
    # descriptions
    "Messages, iMessage, calls, WhatsApp and Viber from an encrypted iPhone backup, made over the cable with libimobiledevice. The backup must be encrypted: only then does it hold calls.":
        "Μηνύματα, iMessage, κλήσεις, WhatsApp και Viber από κρυπτογραφημένο backup του iPhone, με καλώδιο και libimobiledevice. Το backup πρέπει να είναι κρυπτογραφημένο: μόνο τότε έχει τις κλήσεις.",
    "SMS, MMS, calls and blocked numbers of an Android phone, read over adb (USB debugging on).":
        "SMS, MMS, κλήσεις και αποκλεισμένοι αριθμοί ενός κινητού Android, μέσω adb (με USB debugging).",
    "The history Viber Desktop holds (synced from the phone it is linked to), decrypted with scripts/viber-desktop-export.cpp. Linux only: there is no official way.":
        "Το ιστορικό που κρατάει το Viber Desktop (συγχρονισμένο από το κινητό του), αποκρυπτογραφημένο με το scripts/viber-desktop-export.cpp. Μόνο σε Linux: επίσημος δρόμος δεν υπάρχει.",
    "WhatsApp as it arrives, through a whatsmeow bridge linked as a device (Chronika's bridges/whatsapp). Unofficial: WhatsApp may block accounts that use one; sending raises that risk.":
        "Το WhatsApp όπως έρχεται, μέσω γέφυρας whatsmeow συνδεδεμένης ως συσκευή (bridges/whatsapp του Chronika). Ανεπίσημο: το WhatsApp μπορεί να μπλοκάρει λογαριασμούς που τη χρησιμοποιούν· η αποστολή μεγαλώνει το ρίσκο.",
    "Every chat but channels and bots, through Telegram's API with the user's own account (Telethon): the whole history, then live.":
        "Όλες οι συνομιλίες εκτός από κανάλια και bots, μέσω του API του Telegram με τον δικό σου λογαριασμό (Telethon): όλο το ιστορικό, μετά live.",
    "Calls known only from the carrier's SMS notices (\"you have a missed call from ...\"), read from the archive's own SMS, by a parser per carrier or country.":
        "Κλήσεις γνωστές μόνο από τα SMS ειδοποίησης του παρόχου («έχετε αναπάντητη κλήση από …»), από τα SMS του archive, με έναν αναγνώστη ανά πάροχο ή χώρα.",
    "A folder on disk: kept files go into year/month subfolders, named by date and service.":
        "Ένας φάκελος στον δίσκο: τα αρχεία μπαίνουν σε υποφακέλους έτους/μήνα, με όνομα την ημερομηνία και την υπηρεσία.",
    "An immich server, through its API. The API key needs asset.read, asset.view (previews), asset.download (originals) and asset.upload.":
        "Ένας server immich, μέσω του API του. Το API key χρειάζεται asset.read, asset.view (προεπισκοπήσεις), asset.download (πρωτότυπα) και asset.upload.",
    "A vCard file exported from any address book (phone, Google, Outlook, ...).":
        "Ένα αρχείο vCard από οποιοδήποτε βιβλίο διευθύνσεων (κινητό, Google, Outlook, …).",
    "A CardDAV address book: Nextcloud, iCloud, Fastmail, Radicale and others.":
        "Βιβλίο διευθύνσεων CardDAV: Nextcloud, iCloud, Fastmail, Radicale κ.ά.",
    # needs
    "the phone on a USB cable": "το κινητό στο καλώδιο USB",
    "the backup password": "τον κωδικό του backup",
    "USB debugging enabled": "ενεργό USB debugging",
    "a decrypted export (viber-desktop-export.cpp)": "μια αποκρυπτογραφημένη εξαγωγή (viber-desktop-export.cpp)",
    "a running whatsmeow bridge": "μια γέφυρα whatsmeow σε λειτουργία",
    "the bridge's store folder": "τον φάκελο αποθήκευσης της γέφυρας",
    "api_id and api_hash from my.telegram.org": "api_id και api_hash από το my.telegram.org",
    "a login (a code that arrives in Telegram)": "μια σύνδεση (κωδικός που έρχεται στο Telegram)",
    # settings, actions
    "Only with more than one iPhone; otherwise it is found": "Μόνο με πάνω από ένα iPhone· αλλιώς βρίσκεται μόνο του",
    "A new backup before importing": "Νέο backup πριν την εισαγωγή",
    "Off: only decrypt the backup already there": "Κλειστό: μόνο αποκρυπτογράφηση του backup που υπάρχει",
    "Only when adb sees more than one phone": "Μόνο όταν το adb βλέπει πάνω από ένα κινητό",
    "Decrypted database": "Αποκρυπτογραφημένη βάση",
    "The bridge's store folder": "Φάκελος αποθήκευσης της γέφυρας",
    "The bridge's REST API": "REST API της γέφυρας",
    "Sending messages": "Αποστολή μηνυμάτων",
    "A risk for the account; needs the bridge started with -send. Turned off by itself when WhatsApp warns the account":
        "Ρίσκο για τον λογαριασμό· χρειάζεται τη γέφυρα ξεκινημένη με -send. Κλείνει μόνη της όταν το WhatsApp "
        "προειδοποιήσει τον λογαριασμό",
    "sending blocked by the bridge": "η γέφυρα μπλόκαρε την αποστολή",
    "Connection": "Σύνδεση",
    "Sending": "Αποστολή",
    "blocked by the bridge": "μπλοκαρισμένη από τη γέφυρα",
    "off at the bridge (started without -send)": "κλειστή στη γέφυρα (ξεκίνησε χωρίς -send)",
    "off in this source's settings": "κλειστή στις ρυθμίσεις αυτής της πηγής",
    "on, {day} of {limit} today": "ανοιχτή, {day} από {limit} σήμερα",
    "the bridge does not answer": "η γέφυρα δεν απαντά",
    "needs a newer bridge (no /api/status)": "χρειάζεται νεότερη γέφυρα (χωρίς /api/status)",
    "connected to WhatsApp": "συνδεδεμένη στο WhatsApp",
    "not connected to WhatsApp": "χωρίς σύνδεση στο WhatsApp",
    "logged out of WhatsApp": "αποσυνδέθηκε από το WhatsApp",
    "temporarily banned by WhatsApp": "προσωρινός αποκλεισμός από το WhatsApp",
    "another client took the connection": "τη σύνδεση πήρε άλλος client",
    "WhatsApp rejected the bridge's version": "το WhatsApp απέρριψε την έκδοση της γέφυρας",
    "the connection to WhatsApp failed": "η σύνδεση στο WhatsApp απέτυχε",
    "WhatsApp warned the account, sending is off: {why}": "Το WhatsApp προειδοποίησε τον λογαριασμό, η αποστολή έκλεισε: {why}",
    "WhatsApp warned the account": "Το WhatsApp προειδοποίησε τον λογαριασμό",
    "WhatsApp calls (bridge)": "Κλήσεις WhatsApp (γέφυρα)",
    "Check every (seconds)": "Έλεγχος κάθε (δευτερόλεπτα)",
    "Download pictures and videos": "Λήψη φωτογραφιών και βίντεο",
    "Carriers": "Πάροχοι",
    "e.g. gr": "π.χ. gr",
    "Camera make (where missing)": "Κατασκευαστής κάμερας (όπου λείπει)",
    "Address": "Διεύθυνση",
    "If empty, the scripts' immich-key is used": "Αν μείνει κενό, χρησιμοποιείται το immich-key των scripts",
    ".vcf file": "Αρχείο .vcf",
    "Address book URL": "Διεύθυνση βιβλίου διευθύνσεων",
    "User": "Χρήστης",
    "Password (an app password)": "Κωδικός (app password)",
    # statuses
    "ready": "έτοιμο",
    "missing": "λείπει",
    "not found": "δεν βρέθηκε",
    "no answer": "δεν απαντά",
    "api_id and api_hash (scripts/telegram-sync.py --save-credentials)": "api_id και api_hash (scripts/telegram-sync.py --save-credentials)",
    "a login (scripts/telegram-sync.py --login)": "η σύνδεση (scripts/telegram-sync.py --login)",
    "the folder": "ο φάκελος",
    "the address": "η διεύθυνση",
    "the API key": "το API key",
}
TABLES = {"el": EL}


def tr(text, lang="en"):
    if not text or lang == "en":
        return text
    table = TABLES.get(lang, {})
    if text in table:
        return table[text]
    head, sep, rest = text.partition(": ")      # "missing: X, Y" and the like
    if sep and head in table:
        return f"{table[head]}: {', '.join(table.get(p, p) for p in rest.split(', '))}"
    return text
