"""Viber's emoticons as emoji: Viber sends them as a word in parentheses, "(inlove)", "(purple_heart)",
and draws its own picture; elsewhere they are shown as the emoji nearest to it. The list follows
https://github.com/Crissov/unicode-proposals/issues/403 (Viber's emoticons and their Unicode
counterparts), with the newer ones named as Unicode names its emoji ("blue_heart", "party_popper").
A word that is not one of them stays as it was: "(windows)", "(2019)" are text.

    viber_emoji("ok (like)")       # "ok 👍"
"""
import re

VIBER = {
    # faces
    "smiley": "🙂", "sad": "🙁", "wink": "😉", "wink2": "😜", "angry": "😡", "inlove": "😍", "yummi": "😋",
    "laugh": "😆", "surprised": "😮", "moa": "😘", "happy": "😊", "cry": "😭", "crying": "😭", "sick": "🤢",
    "shy": "😳", "teeth": "😁", "tongue": "😛", "money": "🤑", "mad": "😠", "flirt": "😘", "crazy": "🤪",
    "confused": "😕", "depressed": "😔", "scream": "😱", "nerd": "🤓", "not_sure": "😕", "cool": "😎",
    "huh": "😦", "happycry": "😂", "mwah": "😘", "exhausted": "😫", "eek": "😬", "dizzy": "😵", "dead": "😵",
    "straight": "😐", "yo": "😏", "wtf": "😖", "ohno": "😧", "oh": "😯", "what": "🤨", "weak": "😩",
    "upset": "😒", "ugh": "😣", "teary": "🥲", "singing": "😙", "silly": "😝", "meh": "😑", "mischievous": "😏",
    "hmm": "🤔", "eyeroll": "🙄", "lol": "😂", "whatever": "🤷", "devil": "😈", "angel": "😇",
    "ninja": "🥷", "spiderman": "🕷️", "batman": "🦇", "alien": "👽", "robot": "🤖", "ghost": "👻", "skull": "💀",
    # hearts and hands
    "heart": "❤️", "heart_break": "💔", "purple_heart": "💜", "blue_heart": "💙", "yellow_heart": "💛",
    "orange_heart": "🧡", "green_heart": "💚", "black_heart": "🖤", "2_hearts": "💕", "arrow_heart": "💘",
    "heart_lock": "🔐", "kiss": "💋", "like": "👍", "unlike": "👎", "V": "✌️", "clap": "👏", "rockon": "🤘",
    "muscle": "💪", "prayer_hands": "🙏", "waving": "👋",
    # signs
    "angrymark": "💢", "thinking": "💬", "zzz": "💤", "!": "❗", "Q": "❓", "do_not_enter": "⛔",
    "stop_sign": "🛑",
    # things
    "diamond": "💎", "trophy": "🏆", "crown": "👑", "ring": "💍", "$": "💵", "hammer": "🔨", "wrench": "🔧",
    "key": "🔑", "lock": "🔒", "video": "📹", "TV": "📺", "tape": "📼", "trumpet": "🎺", "guitar": "🎸",
    "speaker": "🔊", "music": "🎵", "microphone": "🎤", "bell": "🔔", "telephone": "☎️", "phone": "📱",
    "nobattery": "🪫", "battery": "🔋", "time": "⏰", "knife": "🔪", "syringe": "💉", "termometer": "🌡️",
    "meds": "💊", "ruler": "📏", "scissor": "✂️", "paperclip": "📎", "pencil": "✏️", "magnify": "🔍",
    "glasses": "👓", "book": "📘", "letter": "✉️", "boxing": "🥊", "light_bulb": "💡", "lantern": "🏮",
    "fire": "🔥", "torch": "🔦", "bomb": "💣", "cigarette": "🚬", "gift": "🎁", "cap": "🧢", "fidora": "🎩",
    "partyhat": "🥳", "party_popper": "🎉", "confetti_ball": "🎊", "balloon1": "🎈", "balloon2": "🎈",
    "cards": "🃏", "dice": "🎲", "console": "🎮", "video_game": "🎮", "rocket": "🚀", "airplane": "✈️",
    "ufo": "🛸", "flipflop": "🩴", "relax": "🏖️", "weight": "🏋️",
    # sport
    "golf": "⛳", "golfball": "⛳", "football": "🏈", "tennis": "🎾", "soccer": "⚽", "basketball": "🏀",
    "baseball": "⚾", "8ball": "🎱", "beachball": "🏐", "run": "🏃",
    # animals
    "koala": "🐨", "sheep": "🐑", "ladybug": "🐞", "kangaroo": "🦘", "chick": "🐤", "chicken": "🐔",
    "monkey": "🐵", "panda": "🐼", "turtle": "🐢", "bunny": "🐰", "dragonfly": "🪲", "fly": "🪰", "bee": "🐝",
    "bat": "🦇", "cat": "🐱", "dog": "🐶", "squirrel": "🐿️", "snake": "🐍", "snail": "🐌", "goldfish": "🐠",
    "shark": "🦈", "pig": "🐷", "owl": "🦉", "penguin": "🐧", "paw": "🐾", "fox": "🦊", "octopus": "🐙",
    "dinosaur": "🦖", "parrot": "🦜", "porcupine": "🦔", "poo": "💩",
    # plants and weather
    "cactus": "🌵", "clover": "🍀", "sprout": "🌱", "palmtree": "🌴", "christmas_tree": "🎄", "mapleleaf": "🍁",
    "flower": "🌼", "blue_flower": "🪻", "sunflower": "🌻", "bouquet": "💐", "sun": "☀️", "moon": "🌙",
    "rain": "🌧️", "cloud": "☁️", "umbrella": "☂️", "snowman": "⛄", "snowflake": "❄️", "droplet": "💧",
    "tornado": "🌪️", "universe": "🌌", "star": "⭐",
    # food
    "pizza": "🍕", "cake": "🎂", "cake_slice": "🍰", "cupcake": "🧁", "lemon": "🍋", "popcorn": "🍿",
    "lollipop": "🍭", "sushi1": "🍣", "sushi2": "🍱", "ice_cream": "🍦", "egg": "🥚", "donut": "🍩",
    "burger": "🍔", "peach": "🍑", "apple": "🍎", "banana": "🍌", "beer": "🍺", "wine": "🍷", "coffee": "☕",
    "soda": "🥤", "noodles": "🍜", "bacon": "🥓", "pea": "🫛",
}
_CODE = re.compile(r"\(([A-Za-z0-9_$!]{1,20})\)")


def viber_emoji(text):
    """Viber's "(word)" emoticons in a text as emoji; other parentheses as they are."""
    if not text or "(" not in text:
        return text
    return _CODE.sub(lambda m: VIBER.get(m.group(1), m.group(0)), text)
