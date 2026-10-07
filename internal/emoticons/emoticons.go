// Package emoticons ports everysaid/emoticons.py: Viber's emoticons as emoji. Viber sends them as a
// word in parentheses, "(inlove)", "(purple_heart)", and draws its own picture; elsewhere they are
// shown as the emoji nearest to it. The list follows
// https://github.com/Crissov/unicode-proposals/issues/403 (Viber's emoticons and their Unicode
// counterparts), with the newer ones named as Unicode names its emoji ("blue_heart", "party_popper").
// A word that is not one of them stays as it was: "(windows)", "(2019)" are text.
//
//	ViberEmoji("ok (like)")       // "ok 👍"
package emoticons

import (
	"regexp"
	"strings"
)

var Viber = map[string]string{
	"smiley": "🙂", "sad": "🙁", "wink": "😉", "wink2": "😜", "angry": "😡", "inlove": "😍", "yummi": "😋",
	"laugh": "😆", "surprised": "😮", "moa": "😘", "happy": "😊", "cry": "😭", "crying": "😭", "sick": "🤢",
	"shy": "😳", "teeth": "😁", "tongue": "😛", "money": "🤑", "mad": "😠", "flirt": "😘", "crazy": "🤪",
	"confused": "😕", "depressed": "😔", "scream": "😱", "nerd": "🤓", "not_sure": "😕", "cool": "😎",
	"huh": "😦", "happycry": "😂", "mwah": "😘", "exhausted": "😫", "eek": "😬", "dizzy": "😵", "dead": "😵",
	"straight": "😐", "yo": "😏", "wtf": "😖", "ohno": "😧", "oh": "😯", "what": "🤨", "weak": "😩",
	"upset": "😒", "ugh": "😣", "teary": "🥲", "singing": "😙", "silly": "😝", "meh": "😑",
	"mischievous": "😏", "hmm": "🤔", "eyeroll": "🙄", "lol": "😂", "whatever": "🤷", "devil": "😈",
	"angel": "😇", "ninja": "🥷", "spiderman": "🕷️", "batman": "🦇", "alien": "👽", "robot": "🤖",
	"ghost": "👻", "skull": "💀", "heart": "❤️", "heart_break": "💔", "purple_heart": "💜",
	"blue_heart": "💙", "yellow_heart": "💛", "orange_heart": "🧡", "green_heart": "💚",
	"black_heart": "🖤", "2_hearts": "💕", "arrow_heart": "💘", "heart_lock": "🔐", "kiss": "💋",
	"like": "👍", "unlike": "👎", "V": "✌️", "clap": "👏", "rockon": "🤘", "muscle": "💪",
	"prayer_hands": "🙏", "waving": "👋", "angrymark": "💢", "thinking": "💬", "zzz": "💤", "!": "❗",
	"Q": "❓", "do_not_enter": "⛔", "stop_sign": "🛑", "diamond": "💎", "trophy": "🏆", "crown": "👑",
	"ring": "💍", "$": "💵", "hammer": "🔨", "wrench": "🔧", "key": "🔑", "lock": "🔒", "video": "📹",
	"TV": "📺", "tape": "📼", "trumpet": "🎺", "guitar": "🎸", "speaker": "🔊", "music": "🎵",
	"microphone": "🎤", "bell": "🔔", "telephone": "☎️", "phone": "📱", "nobattery": "🪫", "battery": "🔋",
	"time": "⏰", "knife": "🔪", "syringe": "💉", "termometer": "🌡️", "meds": "💊", "ruler": "📏",
	"scissor": "✂️", "paperclip": "📎", "pencil": "✏️", "magnify": "🔍", "glasses": "👓", "book": "📘",
	"letter": "✉️", "boxing": "🥊", "light_bulb": "💡", "lantern": "🏮", "fire": "🔥", "torch": "🔦",
	"bomb": "💣", "cigarette": "🚬", "gift": "🎁", "cap": "🧢", "fidora": "🎩", "partyhat": "🥳",
	"party_popper": "🎉", "confetti_ball": "🎊", "balloon1": "🎈", "balloon2": "🎈", "cards": "🃏",
	"dice": "🎲", "console": "🎮", "video_game": "🎮", "rocket": "🚀", "airplane": "✈️", "ufo": "🛸",
	"flipflop": "🩴", "relax": "🏖️", "weight": "🏋️", "golf": "⛳", "golfball": "⛳", "football": "🏈",
	"tennis": "🎾", "soccer": "⚽", "basketball": "🏀", "baseball": "⚾", "8ball": "🎱", "beachball": "🏐",
	"run": "🏃", "koala": "🐨", "sheep": "🐑", "ladybug": "🐞", "kangaroo": "🦘", "chick": "🐤",
	"chicken": "🐔", "monkey": "🐵", "panda": "🐼", "turtle": "🐢", "bunny": "🐰", "dragonfly": "🪲",
	"fly": "🪰", "bee": "🐝", "bat": "🦇", "cat": "🐱", "dog": "🐶", "squirrel": "🐿️", "snake": "🐍",
	"snail": "🐌", "goldfish": "🐠", "shark": "🦈", "pig": "🐷", "owl": "🦉", "penguin": "🐧", "paw": "🐾",
	"fox": "🦊", "octopus": "🐙", "dinosaur": "🦖", "parrot": "🦜", "porcupine": "🦔", "poo": "💩",
	"cactus": "🌵", "clover": "🍀", "sprout": "🌱", "palmtree": "🌴", "christmas_tree": "🎄",
	"mapleleaf": "🍁", "flower": "🌼", "blue_flower": "🪻", "sunflower": "🌻", "bouquet": "💐", "sun": "☀️",
	"moon": "🌙", "rain": "🌧️", "cloud": "☁️", "umbrella": "☂️", "snowman": "⛄", "snowflake": "❄️",
	"droplet": "💧", "tornado": "🌪️", "universe": "🌌", "star": "⭐", "pizza": "🍕", "cake": "🎂",
	"cake_slice": "🍰", "cupcake": "🧁", "lemon": "🍋", "popcorn": "🍿", "lollipop": "🍭", "sushi1": "🍣",
	"sushi2": "🍱", "ice_cream": "🍦", "egg": "🥚", "donut": "🍩", "burger": "🍔", "peach": "🍑",
	"apple": "🍎", "banana": "🍌", "beer": "🍺", "wine": "🍷", "coffee": "☕", "soda": "🥤", "noodles": "🍜",
	"bacon": "🥓", "pea": "🫛",
}

var code = regexp.MustCompile(`\(([A-Za-z0-9_$!]{1,20})\)`)

// ViberEmoji is a text with Viber's "(word)" emoticons as emoji; other parentheses as they are.
func ViberEmoji(text string) string {
	if text == "" || !strings.Contains(text, "(") {
		return text
	}
	return code.ReplaceAllStringFunc(text, func(m string) string {
		if e, ok := Viber[m[1:len(m)-1]]; ok {
			return e
		}
		return m
	})
}
