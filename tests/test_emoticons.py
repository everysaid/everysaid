from everysaid.emoticons import viber_emoji


def test_viber_emoticons_as_emoji():
    assert viber_emoji("καλημέρα (inlove)(purple_heart)") == "καλημέρα 😍💜"
    assert viber_emoji("ok (like) (windows) (2019)") == "ok 👍 (windows) (2019)"       # text stays text
    assert viber_emoji("") == "" and viber_emoji(None) is None
