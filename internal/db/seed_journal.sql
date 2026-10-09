-- The first ten Journal articles. Loaded once, while the articles table is
-- empty (see db.SeedJournal). Prices in the text are the listed prices of the
-- sample businesses as of October 2026. Three covers reuse the landing page's
-- hero photos when those exist; the rest have no cover and the screens draw a
-- category motif. Bodies are Markdown, dollar-quoted so nothing needs escaping.

insert into articles (slug, title, dek, body_md, cover_alt, category, tags, author_name, author_role, country, status, published_at, featured, sort, related_category, cta_text, seo_title, seo_description, created_by)
values

-- 1. Knotless braids (featured, cover: hero 1)
($t$knotless-braids-what-to-ask-for$t$,
 $t$Knotless braids: what to ask for, how long they last, and how to care for them$t$,
 $t$Knotless braids lie flat, feel lighter and start without the knot that used to pull. Here is how to book the right ones and keep them fresh for weeks.$t$,
 $md$Knotless braids start with your own hair and feed the extension in a little at a time. There is no knot at the root, so the braid lies flat, moves freely from the first day and puts far less weight on the hairline. That is the whole reason people switched. It is also why they take longer to do and cost more than the braids most of us grew up with.

## What to ask for

Braiders talk in sizes and lengths. Four words settle most of it.

- **Size.** Small, medium or large. Medium is the usual first choice: full enough to look like a lot of hair, light enough to sleep in. Small takes longer and lasts longer. Large is quick, heavier per braid, and best for a short wear.
- **Length.** Shoulder, mid-back or waist. Longer braids need more hair added, which adds weight and time.
- **Parting.** Box parts are the standard. Ask for curved or triangle parts if you want a softer look at the front. Say now if you want a side part or a middle part; it is hard to change once the first row is in.
- **Finish.** Plain ends, curled ends, or boho (loose curly strands left out along the length). Boho is a separate service at most studios because of the extra hair and the extra time.

Bring a photo. One picture does more than ten adjectives, and it lets the braider say honestly whether your hair length and density will give the same result.

## How long the appointment takes

Plan for the whole afternoon. In Nashville, as of October 2026, Ada's Braid Studio books medium knotless at three and a half hours and small at five. Knotless by Nia books medium at the same three and a half hours. Boho adds about half an hour at both. These are working times, not estimates padded for comfort, so eat first and bring a charger.

Most studios ask you to arrive with clean, blow-dried hair. Some offer a wash and blow-dry as an add-on; Ada's lists one at $25. If your hair is not stretched, the braider will spend the first part of your time stretching it, and that time comes out of yours.

## What it costs

Prices depend on the size, the length and whether hair is included. As of October 2026, on LogaLuxe:

- Ada's Braid Studio, Nashville: medium $180, small $240, boho knotless $220. Deposits are $40 to $60.
- Knotless by Nia, Nashville: medium $160, boho $200, with a $40 deposit.
- Peachtree Locs & Braids, Atlanta: medium $190, with the hair included.

Ask whether the price includes hair. If it does not, the braider will tell you how many packs to bring and which brand they prefer to work with.

## How long they last

Six to eight weeks is the honest answer for medium braids worn with care. Small braids can go a little longer because each one holds less hair. The hairline shows wear first: new growth lifts the braid away from the scalp and the parts begin to blur. When you can see more than about an inch of new growth at the root, it is time to take them down. Pushing past that point is where thinning edges come from.

## Aftercare that actually matters

- Sleep in a satin or silk scarf or bonnet. Cotton pillowcases pull moisture out and roughen the braid.
- Oil the scalp lightly twice a week. A little on the fingertips along the parts is enough; soaking the braids makes them heavy and attracts lint.
- Wash every two to three weeks. Dilute shampoo in a bottle, apply it to the scalp, rinse well and let the braids dry fully, in the sun or with a dryer on cool. Damp braids left wrapped smell, and the smell does not come out.
- Keep tension off the hairline. Avoid tight ponytails for the first week, and never sleep in a high bun.
- Do not re-dip or re-curl the ends yourself with boiling water unless the braider showed you how.

## When something is wrong

Small bumps along the hairline on day one or two mean the braids are too tight. Message your braider the same day; a good one will loosen the front rows without charging. Itching that starts after a week is usually a dry scalp and an oil fixes it. Itching with flaking or soreness is not, and the braids should come out.

## Booking on LogaLuxe

Every braider on LogaLuxe lists sizes, prices and times, and the first free appointments. Pick one near you, send the photo in the notes, and the deposit holds your afternoon: [find a braider near you](/search?category=braids). If you wear a silk press between installs, read [Silk press care](/journal/silk-press-care).$md$,
 $t$A woman with long knotless braids and a cream silk top, lit in warm gold against a deep red backdrop$t$,
 'braids', '{knotless,braids,aftercare,"what to ask for"}', 'LogaLuxe editorial', 'Editorial team', '', 'published', timestamptz '2026-10-05 09:00+00', true, 1, 'braids', 'Find a braider near you',
 $t$Knotless braids: what to ask for and how to care for them$t$,
 $t$Sizes, lengths, partings and finishes explained, how long the appointment takes, what it costs on LogaLuxe as of October 2026, and the aftercare that makes them last.$t$,
 'seed'),

-- 2. Gel or acrylic nails (featured, cover: hero 3)
($t$gel-or-acrylic-nails-which-to-book$t$,
 $t$Gel or acrylic nails: which to book, and what each one asks of you$t$,
 $t$Gel and acrylic are not the same service. The right one depends on your nails, your hands and how long you want them to last.$t$,
 $md$Walk into a nail bar and ask for "a set" and you will be asked a question back: gel or acrylic? They look alike in a photo. On your hands, over three weeks, they behave very differently. This is the plain version of the difference, so you can book the right thing the first time.

## What each one is

**Gel** is a polish-like product cured hard under a UV or LED lamp. A gel manicure goes straight onto your own nail. It adds shine and colour and a little strength, and it keeps the length you already have. Builder gel and gel extensions add length with a soft tip, but they are still gel in how they feel and how they come off.

**Acrylic** is a powder and a liquid mixed on the brush and shaped over your nail or over a tip. It sets hard in the air, no lamp needed, and it is the strongest thing a nail technician can put on you. A full set is sculpted, so almost any length and shape is possible.

## Which one suits you

Choose gel if:

- your natural nails are a decent length and you mainly want colour that does not chip
- you type all day, cook, or work with your hands and want something you can forget about
- your nails are thin or peeling and need a break from anything heavy
- you want to be out of the chair in under an hour

Choose acrylic if:

- you want real length and your own nails will not get there
- you want a long stiletto, coffin or square shape that has to hold its line
- you are hard on your hands and need something that will not bend
- you are happy to come back every two to three weeks for a fill

Neither is healthier than the other on its own. Nails are damaged by rough removal and by picking, not by the product. A good technician and a patient removal are what protect them.

## How long each takes, and what it costs

As of October 2026, Crenshaw Nail Bar in Los Angeles lists a gel manicure at $45 and 50 minutes, and an acrylic full set at $75 and 90 minutes with a $20 deposit. Those times are typical: gel is a quick service, a sculpted set is not. A pedicure there is $40.

Fills on acrylic are usually cheaper than a new set and take a little less time. Ask when you book. A gel manicure is redone each time rather than filled.

## How long they last

A gel manicure on healthy nails holds two to three weeks before the growth at the base shows. Acrylic lasts as long as you keep up the fills; the set itself can stay on for months, with a new set every three or four fills so the technician can check the natural nail underneath.

## Removal, and why it matters more than the application

This is where nails get hurt.

- Gel soaks off in acetone in about fifteen minutes. The technician files the shine away first, wraps each finger, waits, and then pushes the softened gel off gently. Peeling gel off at home takes the top layer of your nail with it.
- Acrylic takes longer: the bulk is filed down first, then the rest is soaked. Thirty to forty minutes is normal. Do not let anyone pry a set off with a tip.

Book the removal as its own service, or say when you book that you have a set on. It changes the time your technician needs.

## Looking after either one

- Oil the cuticles every night. A dry cuticle lifts the edge of gel and lets water under acrylic.
- Wear gloves for cleaning. Bleach and hot water shorten the life of both.
- Do not use your nails as tools. The one that breaks will take some natural nail with it.
- If a nail lifts, do not glue it at home. Water gets trapped under a glued nail, and that is how infections start. Message your technician.

## Booking on LogaLuxe

Nail bars on LogaLuxe list gel and acrylic as separate services with their own times, so pick the one you want and the right slot is booked. Add a photo of the shape and length in the notes. [Find a nail technician near you](/search?category=nails). For how deposits and cancellations work before you book, read [How deposits and cancellations work on LogaLuxe](/journal/deposits-and-cancellations-on-logaluxe).$md$,
 $t$A nail technician with long braids painting a client's nails in a warm burgundy and gold salon$t$,
 'nails', '{gel,acrylic,manicure,removal}', 'LogaLuxe editorial', 'Editorial team', '', 'published', timestamptz '2026-10-02 09:00+00', true, 2, 'nails', 'Find a nail technician near you',
 $t$Gel or acrylic nails: which to book$t$,
 $t$What gel and acrylic each are, which suits your hands, how long they take and last, the removal that protects your nails, and prices on LogaLuxe as of October 2026.$t$,
 'seed'),

-- 3. What a first visit costs (featured, cover: hero 2)
($t$what-a-first-visit-costs-in-nashville-and-lagos$t$,
 $t$What a first visit costs in Nashville and in Lagos$t$,
 $t$The same services in two cities, with real listed prices from professionals on LogaLuxe as of October 2026, and what the numbers include and leave out.$t$,
 $md$People ask us this more than anything else: what should I expect to pay? It is a fair question and a hard one to answer in general, because prices depend on the person, the neighbourhood and what exactly you are asking for. So here is the specific version. These are the listed prices of professionals on LogaLuxe in Nashville and in Lagos, as they stand in October 2026. Prices change; the booking page always has the current one.

## Braids

In Nashville, Ada's Braid Studio in East Nashville lists medium knotless braids at $180 for three and a half hours, small knotless at $240 for five hours, and boho knotless at $220. Deposits run from $40 to $60. Knotless by Nia in Antioch lists medium knotless at $160 and boho at $200, with a $40 deposit.

Ask whether hair is included. In Atlanta, for comparison, Peachtree Locs & Braids lists medium knotless at $190 with the hair included, which closes most of the gap with a $160 install where you bring your own packs.

## A barber

The Barber Loft in the Gulch lists a skin fade at $35 for 45 minutes, a skin fade with beard work at $45, a line up at $20 and a kids cut at $25. No deposit is asked.

In Lagos, Freedom Way Barbers in Lekki lists a skin fade at ₦7,000 for 40 minutes and a fade with beard at ₦9,000 for 45 minutes. Again, no deposit.

The two shops are doing the same forty-odd minutes of work. The difference is the economy around them, not the skill in the chair.

## Skin and spa

Glow Skin Bar in 12 South lists a signature facial at $95 for an hour, a chemical peel at $120 for 45 minutes with a $40 deposit, and a brow shape at $25.

In Lagos, MnM Spa Parlour in Lekki Phase 1 lists a facial at ₦25,000 for 45 minutes. It also lists a full body massage at ₦30,000 for an hour and a hot stone massage at ₦40,000 for 75 minutes, each with a ₦10,000 deposit.

## Hair

Ada's Braid Studio lists a silk press at $75 for 90 minutes with a $20 deposit, and a loc retwist at $95. Add-ons there are a wash and blow-dry at $25, a take-down at $30 and a scalp treatment at $25.

## What the number includes

A listed price on LogaLuxe is the price of the service for the time shown. It does not include:

- **Hair or product you bring.** For braids this is the big one. Ask, and if hair is not included, ask how many packs.
- **Add-ons.** A take-down before an install, a wash, a treatment. They are listed separately so you can see them before you book, and most studios will add them on the day if there is time.
- **A tip.** In the United States a tip is customary and goes to the person who did the work; LogaLuxe lets you add one afterwards from your booking. In Nigeria it is welcome and not expected.

## What the deposit is for

A deposit is part of the price, paid when you book, and comes off the total on the day. It is not an extra. It holds the time, and for a long appointment like a five-hour install it is what lets a braider turn down other requests for that afternoon. If you cancel before the business's free cancellation window closes, it comes back to you. [How deposits and cancellations work on LogaLuxe](/journal/deposits-and-cancellations-on-logaluxe) has the full rules.

## Comparing the two cities honestly

A dollar price and a naira price are not the same kind of number, and converting at the day's rate tells you less than it seems to. What matters is the price relative to the city you are in. In both cities the pattern is the same: cuts and brows are quick and inexpensive, facials and massages sit in the middle, and braids are the long, skilled, expensive service, because they take an afternoon of one person's hands.

The useful move is the same in both places: open two or three profiles in your category, read the times as well as the prices, and book the one whose work you like. [Find a professional near you](/search).$md$,
 $t$A woman with long braids in a cream blouse, looking to the side against a burgundy backdrop$t$,
 'guide', '{prices,nashville,lagos,"first visit",deposits}', 'LogaLuxe editorial', 'Editorial team', '', 'published', timestamptz '2026-09-30 09:00+00', true, 3, null, 'Find a professional near you',
 $t$What a first visit costs in Nashville and in Lagos$t$,
 $t$Listed prices for braids, a fade, a facial and a silk press from professionals on LogaLuxe in both cities as of October 2026, and what a price does and does not include.$t$,
 'seed'),

-- 4. Choosing a barber and describing a fade
($t$how-to-choose-a-barber-and-describe-a-fade$t$,
 $t$How to choose a barber, and how to describe the fade you want$t$,
 $t$A good cut starts with the right chair and five clear words. Here is how to find the one and say the other.$t$,
 $md$Most bad haircuts are not bad barbers. They are a mismatch: the wrong shop for your hair, or the right shop and the wrong words. Both are fixable before you sit down.

## Choosing the shop

Look at the work, not the decor. On LogaLuxe a barber's page shows photos, reviews and the services with their times. Three things to check:

- **Your hair type is in the photos.** A barber who posts only one texture is telling you what they do most. If your hair is coily, look for coily hair in the pictures; if it is straight and fine, look for that.
- **The time listed for a fade.** A skin fade done well is a 40 to 45 minute job. As of October 2026, The Barber Loft in Nashville books a skin fade at 45 minutes, Bayou Fade Co. in Houston at 45, and Freedom Way Barbers in Lagos at 40. A fade listed at 15 minutes is a quick fade.
- **Reviews that mention a second visit.** "I keep coming back" is worth more than "great cut".

Pick a person, not a shop, when you can. Shops list each barber on LogaLuxe, and the booking is with that person.

## The words that describe a fade

A fade is hair that gets shorter as it goes down. Everything else is detail, and the detail is what you need to say.

**How high it starts.** This is the point on the head where the fade begins.

- *Low* starts just above the ear and the neckline. Clean and conservative.
- *Mid* starts around the temple. The most common ask.
- *High* starts near the top of the head. Bold, and it grows out faster.

**How short it goes at the bottom.**

- *Skin* (also called bald) takes it down to the skin with a razor or a foil shaver.
- *Zero* leaves the shortest clipper length without a razor.
- A *taper* only shortens around the ears and the neck and leaves the rest of the sides long. It is the gentlest version and the easiest to grow out.

**What happens on top.** Say the length in inches or show a photo. "Leave the top, take the sides" is a full instruction to most barbers. If you want it textured, scissored, or a hard part, say so now.

**The line.** A line up (also called an edge up or a shape up) squares the hairline at the forehead and the temples. It is often a separate service: The Barber Loft lists it at $20, and some shops include it in a fade. Ask.

Put together, a full instruction sounds like this: "Mid skin fade, leave about two inches on top, line up, and square the back." That is all a barber needs.

## The photo rule

Bring one photo of a cut you like on hair like yours. One. A folder of ten photos with different fades is a conversation, not an instruction. If the photo is of someone with a different hair texture, your barber will tell you what will and will not translate, and you should listen.

## Beard work

Beard work is a separate skill and usually a separate line on the menu. The Barber Loft lists a skin fade with beard at $45 against $35 without; Freedom Way Barbers lists a fade with beard at ₦9,000 against ₦7,000. Say whether you want the beard shaped, faded into the sideburns, or just lined.

## On the day

- Arrive with your hair as you normally wear it, not freshly gelled. The barber needs to see how it falls.
- Say if it is your first time in that chair. A good barber will go a little longer than you asked for the first cut, so there is room to adjust.
- Look in the mirror before the cape comes off. Ask for the back to be shown. This is the moment to say "a little shorter on the left"; after you have paid is not.

## Keeping it

A skin fade looks sharp for about ten days and good for three weeks. Book the next one before you leave; most barbers on LogaLuxe show their free times for the weeks ahead, and a repeat booking takes a minute.

[Find a barber near you](/search?category=barber).$md$,
 '', 'barber', '{fade,barber,"skin fade","line up",beard}', 'LogaLuxe editorial', 'Editorial team', '', 'published', timestamptz '2026-09-28 09:00+00', false, 4, 'barber', 'Find a barber near you',
 $t$How to choose a barber and describe a fade$t$,
 $t$How to pick the right chair from photos, times and reviews, and the five words that describe a fade so you get the cut you asked for.$t$,
 'seed'),

-- 5. Lash extension care
($t$lash-extension-care$t$,
 $t$Lash extension care: the first 48 hours and the four weeks after$t$,
 $t$Extensions are a small daily habit as much as a service. Here is what to do, what to avoid, and when to go back.$t$,
 $md$Lash extensions are single fibres, or small fans of them, glued one by one to your own lashes. Done well, they look like you were born with them and they last as long as the natural lash they are attached to. The glue is the whole thing: how it sets in the first day, and how you treat it after, decides whether you get four good weeks or ten patchy days.

## Classic or volume

Two services, two looks.

- **Classic** puts one extension on one natural lash. The result is longer and darker lashes that still look like lashes. It takes about ninety minutes.
- **Volume** puts a small fan of two to six very fine extensions on each natural lash. The result is fuller and softer. It takes about two hours, and a full set costs more.

As of October 2026, Wuse Lash Lounge in Abuja lists a classic set at ₦25,000 for 90 minutes with a ₦5,000 deposit, and a volume set at ₦35,000 for 120 minutes with a ₦10,000 deposit. The times are honest ones; a full set done in forty minutes is not a full set.

If your natural lashes are sparse or fine, say so when you book. A technician will often suggest a light volume set rather than classic, because the fans weigh less than a single thicker fibre.

## The first 48 hours

Glue cures with moisture over about two days. Until then, treat the lashes as wet paint.

- Keep them dry. No showers over the face, no steam, no swimming, no heavy workouts. Wash your face around the eyes with a cloth.
- No oil near the eyes. Oil-based cleansers, balms and heavy creams break the bond.
- Do not touch, rub or pick. The urge is strongest on day one.
- Sleep on your back if you can. A silk pillowcase helps if you cannot.
- No mascara. Not now, not later. It clumps on extensions and the remover is oil.

## The four weeks after

Once cured, extensions want to be clean. The single biggest cause of early loss is not washing them.

- **Wash them daily** with a lash-safe foaming cleanser and a soft brush. Work gently from the base to the tip, rinse with water and pat dry. Oil and dead skin at the lash line loosen the glue and can inflame the lid.
- **Brush them every morning** with the spoolie you were given. It separates the lashes and keeps them lying the right way.
- **Keep oil away.** Check your cleanser, your eye cream and your sunscreen. If the ingredient list has an oil near the top, it does not go near the lash line.
- **Avoid steam.** Saunas and the steam from cooking over a pot loosen the bond over time. A short shower is fine once the first two days have passed.
- **Do not pull them.** A lash that is ready to fall will come away on its own with its natural lash. One pulled early takes the natural lash with it, and that one takes six to eight weeks to grow back.

## Refills

Natural lashes shed on a cycle. You lose a few every day, and the extensions go with them. A refill replaces what has gone. Book one every two to three weeks; after four weeks most technicians will call it a new set, because there is too little left to fill. Arrive with clean lashes; a technician who has to clean them first spends your time doing it.

## When to stop

If the lids are red, itchy or swollen, do not wait it out. Wash with the cleanser, skip the oil, and if it is not better by the next morning, have the extensions removed professionally. A reaction to the glue is uncommon and it is not something to push through. Removal is quick and painless when a technician does it with the proper remover. At home, with tweezers, it is neither.

Give your natural lashes a break every few months if they feel thin. They will recover.

## Booking on LogaLuxe

Lash studios on LogaLuxe list classic, volume and refills separately with their times, so the right slot is booked for the right work. Say in the notes if it is your first set or if you are coming in with a set to be refilled. [Find a lash technician near you](/search?category=lashes).$md$,
 '', 'lashes', '{lashes,extensions,aftercare,refills}', 'LogaLuxe editorial', 'Editorial team', '', 'published', timestamptz '2026-09-25 09:00+00', false, 5, 'lashes', 'Find a lash technician near you',
 $t$Lash extension care: the first 48 hours and after$t$,
 $t$Classic or volume, what to avoid while the glue cures, the daily wash that makes a set last, when to refill, and when to have them taken off.$t$,
 'seed'),

-- 6. Skin care for humid Lagos (Nigeria)
($t$skin-care-for-humid-lagos$t$,
 $t$A skin-care routine for humid Lagos$t$,
 $t$Heat, humidity and dust ask different things of your skin than a cool climate does. A short routine that works on the Island or the Mainland, morning and night.$t$,
 $md$Most skin-care advice is written for cool, dry places. It tells you to layer creams, and in Lagos a layered cream has slid off your face by ten in the morning. Humidity means your skin is rarely dry, but it is also rarely clean: sweat, sunscreen, dust from the road and the oil your skin makes all day end up in the same place. A routine for Lagos is short, light and done twice a day without fail.

## Morning

**1. Cleanse.** A gentle gel or foaming cleanser, with water. Skip the scrub. If your skin is oily, a cleanser with a little salicylic acid helps keep pores clear; use it every morning and watch for tightness.

**2. Treat, if you treat.** A vitamin C serum in the morning helps with the uneven tone that sun and old breakouts leave behind. Three or four drops, pressed in, is enough. If it stings, it is too strong for you right now.

**3. Moisturise lightly.** A gel moisturiser or a light lotion. Your skin still needs moisture; it just does not need oil on top. If your skin is dry in patches, use the lotion only there.

**4. Sunscreen.** This is the step. Broad spectrum, SPF 30 at least, every single morning, including the days you spend indoors near a window. On dark skin a sunscreen that leaves no white cast matters, and there are many now that do not. Reapply at midday if you are outside. Sun is what deepens dark marks and what makes them last.

## Night

**1. Cleanse twice.** First with a cleansing balm or micellar water to lift sunscreen and the day's grime, then with your morning cleanser. This is the single change most people in Lagos benefit from. One wash does not get sunscreen off.

**2. Treat.** This is where the stronger things go, because there is no sun to argue with them.

- For breakouts: a leave-on salicylic acid or benzoyl peroxide, on the areas that break out, not all over.
- For dark marks: niacinamide or azelaic acid. Both are kind to dark skin and do not bleach it.
- For texture and fine lines: a retinoid, starting twice a week and working up. Retinoids make skin more sensitive to sun, which is another reason the morning sunscreen is not optional.

Pick one. Adding three at once is how people end up with a burning face and no idea which product did it.

**3. Moisturise.** The same light moisturiser, or something a touch richer if your skin feels tight after the treatment.

## Weekly

- **A clay mask once a week** if your skin is oily. Ten minutes, rinse well.
- **A gentle chemical exfoliant** (lactic or mandelic acid) once a week if you are not already using a retinoid. Never a physical scrub on acne.

## What to stop doing

- Stop using bleaching creams and "toning" lotions. Many contain hydroquinone or steroids at doses that thin the skin and leave it blotchy and fragile. The even tone they promise is the one a sunscreen and a niacinamide give you slowly and safely.
- Stop washing with hot water. Lukewarm.
- Stop using heavy shea butter on the face. It is wonderful on the body and too much for most faces in this climate.
- Stop touching your face in traffic. Your hands have been on the door, the rail and the money.

## When to see a professional

A facial every four to six weeks is a reasonable rhythm here. Deep cleansing and extractions done properly clear what the daily routine cannot, and a skin professional will see the beginnings of a problem before you do. As of October 2026, MnM Spa Parlour in Lekki Phase 1 lists a facial at ₦25,000 for 45 minutes. Stubborn acne, melasma or a rash that will not shift are a dermatologist's work, not a spa's, and a good spa will say so.

Before a peel of any kind, ask for a patch test. On dark skin, a peel done too strong leaves marks that last longer than what it was meant to fix.

## Booking on LogaLuxe

Skin professionals on LogaLuxe list each treatment with its time and price, and you can read what other clients said before you book. [Find a skin specialist near you](/search?category=skin).$md$,
 '', 'skin', '{skin,lagos,sunscreen,routine,"dark marks"}', 'LogaLuxe editorial', 'Editorial team', 'NG', 'published', timestamptz '2026-09-22 09:00+00', false, 6, 'skin', 'Find a skin specialist near you',
 $t$A skin-care routine for humid Lagos$t$,
 $t$A morning and night routine for heat, humidity and dust: the cleanse, the one treatment to pick, the sunscreen that is not optional, and what to stop doing.$t$,
 'seed'),

-- 7. Bridal makeup timing
($t$bridal-makeup-timing$t$,
 $t$Bridal makeup timing: the months before and the morning itself$t$,
 $t$The makeup is the last thing before the dress and the first thing in every photo. When to book, when to trial, and how to plan the morning so nothing is rushed.$t$,
 $md$Wedding makeup is not a long service. A full bridal look takes about two hours in the chair. What goes wrong is almost never the makeup; it is the schedule around it. An artist who is booked for nine and arrives to find the bride still in the shower at ten is doing the same two hours of work with half the calm.

## The months before

**Six months out: book the artist.** Good artists are booked a season ahead, and a Saturday in the dry season in Port Harcourt or Lagos, or a Saturday in June in Nashville, goes early. As of October 2026, Garden City Makeup in Port Harcourt lists bridal makeup at ₦80,000 for two hours with a ₦30,000 deposit. The deposit is what holds the date. Pay it when you book.

**Three months out: the trial.** A trial is a full run of the look, with time to talk. Garden City Makeup lists a bridal trial at ₦35,000 for 90 minutes. Book it for a day when you have somewhere to go afterwards, in daylight, and wear a top in the colour of the dress. Take photos outside, in the shade and in the sun, with your phone and no filter. Then wear the makeup all day and notice what happens at hour six.

**Two months out: decide and tell the artist.** More or less coverage, a different lip, a change to the lashes. The trial is for changing things; the morning is not.

**One month out: the schedule.** Write down who is being made up and in what order, and send it to the artist. Bridesmaids and mothers usually take 45 to 60 minutes each. The bride goes last among the party, so her makeup is freshest, but not so last that the dress waits for her.

## The morning

Work backwards from the time you must be in the dress.

- **Dress on:** thirty minutes before leaving.
- **Bride's makeup:** two hours before the dress. Garden City's two hours is typical.
- **Hair:** usually before makeup, so the artist is not working around a stylist's hands and heat.
- **Bridal party:** before the bride, in order of who has the most to do afterwards.
- **Artist arrives:** fifteen minutes before the first face, to set up and to see the light in the room.

Add the travel time to the venue, and then add thirty minutes you will not believe you need. You will need it.

## Setting up the room

- Daylight, a window, and a chair with no arms. Most artists bring their own light, but a window is better.
- A table at waist height for the kit.
- The room where the makeup happens is not the room where people are getting dressed, drinking or doing hair. Ask for one quiet corner.
- Food and water for the bride before she sits down. Lipstick after a sandwich is fine; a sandwich after lipstick is not.

## Skin, the week before

Nothing new. No new product, no facial you have not had before, no peel, no waxing of the face within five days. If you have a monthly facial, the last one should be ten days out, so any redness is gone. Drink water, sleep, and leave the spots alone; an artist can cover a spot but not a wound.

## On the day, for the bride

- Arrive at the chair with a clean, moisturised face and nothing else on it.
- Wear a button-down shirt or a robe. A T-shirt pulled over a finished face is a story every artist can tell.
- Ask for a small bag: the lipstick, a powder, blotting paper. Someone sensible carries it.
- Show your maid of honour the touch-up kit before the artist leaves, in case of tears.

## Event makeup, if it is not your wedding

For a guest or a traditional ceremony, event makeup is a shorter service. Garden City Makeup lists it at ₦30,000 for 75 minutes with a ₦10,000 deposit. The same rules hold: book early for a Saturday, arrive with a clean face, and leave a little time.

## Booking on LogaLuxe

Makeup artists on LogaLuxe list bridal, trial and event makeup as separate services with their own times, so the right slot is held. Put the wedding date and the venue in the notes when you book the trial. [Find a makeup artist near you](/search?category=makeup).$md$,
 '', 'makeup', '{bridal,makeup,wedding,trial,timing}', 'LogaLuxe editorial', 'Editorial team', '', 'published', timestamptz '2026-09-19 09:00+00', false, 7, 'makeup', 'Find a makeup artist near you',
 $t$Bridal makeup timing: before and on the day$t$,
 $t$When to book the artist, when to do the trial, the morning schedule worked backwards from the dress, and how to set up the room.$t$,
 'seed'),

-- 8. Deposits and cancellations (guide)
($t$deposits-and-cancellations-on-logaluxe$t$,
 $t$How deposits and cancellations work on LogaLuxe$t$,
 $t$What a deposit is, where the money goes, when you get it back, and how to move a booking without losing it.$t$,
 $md$A booking on LogaLuxe is an agreement between you and a professional about a block of their time. The deposit and the cancellation window are the two rules that make the agreement fair in both directions. Here is exactly how they work, with nothing left to guess.

## Why some services ask for a deposit

A braider who books you for five hours on a Saturday turns away everyone else who wanted that afternoon. If you do not come, the afternoon is gone and so is the income. A deposit is the professional's protection against that, and it is also your guarantee that the time is held for you and nobody else.

Not every service asks for one. Quick services like a line up or a brow shape usually do not. Long or prepared-for services, like a full braid install, a chemical peel or bridal makeup, usually do. The amount is set by the professional and shown on the service before you book. As of October 2026, a medium knotless install at Ada's Braid Studio in Nashville carries a $40 deposit on a $180 service; bridal makeup at Garden City Makeup in Port Harcourt carries a ₦30,000 deposit on ₦80,000.

A professional can also ask for a deposit from a first-time client on a service that does not usually need one, and can ask someone who missed a visit before to pay in full up front. Both are shown before you confirm.

## What happens when you book

1. You choose the service, the person and the time.
2. If a deposit is due, you are taken to a payment page: a card through Stripe in the United States, and through Paystack in Nigeria. The time is held for 30 minutes while you pay.
3. When the payment arrives, the booking is confirmed and you get an email with the details. A calendar file is on the booking page. If the payment does not arrive within the 30 minutes, the booking is cancelled and the time is released for someone else. Nothing is charged.

Some professionals confirm each booking themselves instead of at once; your booking then shows as a request until they accept it. The deposit is held the same way.

The deposit is part of the price, not an extra. On the day, you pay the rest.

## The free cancellation window

Every professional sets a window: a number of hours before the appointment during which you can cancel or move it for free. Most use 24 hours; it can be anything from none to a week. It is shown on the professional's page and on your booking.

- **Cancel before the window closes:** the deposit is refunded in full to the card you paid with, through the same provider. It can take a few days to show on your statement, which is the bank's timing, not ours.
- **Cancel after the window closes:** the professional may keep the deposit. Whether they do is their policy, set in advance and shown on your booking as the late cancellation fee. Many keep it; some do not.
- **Do not come and do not cancel:** that is a no-show. The deposit is usually kept, and some professionals will ask you to pay in full before any future booking.

## Moving a booking instead of cancelling it

While the window is still open, you can move your booking yourself from your account: open the booking, choose a new time from the professional's free ones, and the price you agreed stays the same. The deposit moves with it. This is almost always better than cancelling and booking again, for both of you.

Once the window has closed, moving is at the professional's discretion. Message them from the booking. Most will help when they can, and the earlier you ask, the more they can do.

## When the professional cancels

If the professional cancels, your deposit comes back in full, whatever the window says. You will get an email, and the booking will show as cancelled by the business.

## If something goes wrong

If you think a deposit was kept when it should not have been, or a refund has not arrived after a week, open the booking and report a problem. The professional is asked to answer it, and if that does not settle it, write to LogaLuxe from the contact page.

## The short version

- A deposit holds the time and comes off the price.
- You have 30 minutes to pay it.
- Cancel or move before the window closes and nothing is lost.
- After the window, the professional may keep the deposit.
- Move a booking rather than cancelling when you can.

Ready to book? [Find a professional near you](/search). For what first visits cost in two cities, read [What a first visit costs in Nashville and in Lagos](/journal/what-a-first-visit-costs-in-nashville-and-lagos).$md$,
 '', 'guide', '{deposits,cancellations,booking,refunds,"how it works"}', 'LogaLuxe editorial', 'Editorial team', '', 'published', timestamptz '2026-09-16 09:00+00', false, 8, null, 'Find a professional near you',
 $t$How deposits and cancellations work on LogaLuxe$t$,
 $t$Why some services take a deposit, the 30 minutes to pay, the free cancellation window, when a deposit comes back, and how to move a booking instead of cancelling it.$t$,
 'seed'),

-- 9. More rebookings (for professionals)
($t$how-to-get-more-rebookings$t$,
 $t$How to get more rebookings$t$,
 $t$The cheapest client to win is the one in your chair. Six habits that turn a first visit into a standing appointment, using the tools already in your LogaLuxe account.$t$,
 $md$A business with a full book is rarely the one with the most new clients. It is the one whose clients come back. New clients cost time to find and time to learn; a returning client knows the drill, trusts the work and tells their friends. Rebooking is the whole game, and it is mostly habit.

## 1. Book the next visit before this one ends

The single most effective thing, and the one most often skipped. While the client is in the chair and happy with the result, say when they should come back and offer a time: "Your fade will want a tidy in three weeks. I have Saturday the 1st at 10 or Tuesday the 4th at 6." Then book it on the spot.

On LogaLuxe you can take the booking from your calendar for them, or they can repeat the visit from their own account in two taps, with the same service and the same person. Either way, do it before they leave. A client who walks out with the next appointment set comes back; a client who says "I will book online" often does, two weeks late.

## 2. Make the window and the deposit clear

Clients return to businesses they understand. Set a cancellation window you can live with, 24 hours for most services, and a deposit on anything over an hour. State both on your page. A client who knows the rules does not feel caught out, and a business that is not losing afternoons to no-shows has the calm to do good work. The first-visit deposit setting lets you ask new clients only, so regulars are not bothered.

## 3. Answer messages the same day

Your inbox on LogaLuxe is where rebookings are won and lost between visits. "Can you fit me in Thursday?" answered at nine in the evening is a booking; answered three days later is a client who went elsewhere. Saved replies cover the questions you answer ten times a week, and a drafted reply from the assistant gives you a starting point you can send as it is or change. Nothing goes out until you send it.

## 4. Say thank you, and mean it

A short message the day after a visit does more than any discount. Not a campaign, a message: "Thanks for coming in yesterday, the colour looked great. Sleep with the scarf." It shows the client that the person who did their hair is still thinking about their hair. Write it in your own words.

## 5. Bring back the ones who drifted

Every book has clients who were regular and then were not. Your marketing page lists them for you: clients who have not visited in sixty days. Send them one message, by WhatsApp or email, with your booking link. Something plain: "It has been a while. I have a few openings next week if you want the usual." The campaign tool puts their first name and your link in for you; you write the sentence in between.

Do not send a discount to a lapsed client as the first move. It tells them the full price was too high, and it is harder to go back up than you think. Send the discount if the plain message gets no answer.

## 6. Give regulars a reason that is not a discount

Loyalty points on LogaLuxe reward each visit and can be spent on a service or an add-on. A package of four visits at a small saving, paid up front, is four rebookings in one decision. A monthly membership for the clients who come every month anyway turns them into income you can plan around. These work because they match what the client already wanted to do; they do not work on someone who was never coming back.

## What to measure

Your reports page counts how many of the month's visits were from returning clients. Watch that number, not the number of new ones. If it is climbing, your work and your follow-up are doing their job. If it is flat while your new-client number is high, you are filling a bucket with a hole in it, and the hole is one of the six habits above.

## Two things that quietly lose rebookings

- **Running late without saying so.** A client who waited forty minutes forgives it once. Tell them by message as soon as you know.
- **Changing the price on the day.** The price agreed when booking is the price. If an add-on is needed, say so and offer the choice before you start.

Everything here is already in your LogaLuxe account: the calendar, the inbox, saved replies, campaigns, loyalty, packages, memberships and reports. The habit is the part only you can add. For the client's side of the rules you set, read [How deposits and cancellations work on LogaLuxe](/journal/deposits-and-cancellations-on-logaluxe).$md$,
 '', 'business', '{rebookings,retention,"for professionals",inbox,loyalty}', 'LogaLuxe editorial', 'Editorial team', '', 'published', timestamptz '2026-09-12 09:00+00', false, 9, null, 'List your business on LogaLuxe',
 $t$How to get more rebookings$t$,
 $t$Six habits for beauty professionals that turn a first visit into a standing appointment: book before they leave, clear rules, fast replies, a thank you, win-backs and loyalty.$t$,
 'seed'),

-- 10. Silk press care
($t$silk-press-care$t$,
 $t$Silk press care: how to keep it sleek and your curls safe$t$,
 $t$A silk press is heat, skill and a week of small decisions. How to book one, what to ask, and how to make it last without damage.$t$,
 $md$A silk press is natural hair washed, blow-dried and flat-ironed straight, with no chemical and no relaxer. Done well it moves, it shines and it reverts fully to your curl at the next wash. Done badly it leaves pieces that never curl again. The difference is the heat, the hands and what you do in the days after.

## Booking it

The service is longer than people expect. As of October 2026, Ada's Braid Studio in Nashville lists a silk press at $75 for 90 minutes with a $20 deposit. Most of that time is the wash and the blow-dry; the flat iron is the last twenty minutes. If your hair is long, thick or very coily, say so when you book so the stylist can allow more time.

Ask three things:

- **What heat do you use?** A careful stylist works between 350 and 400 degrees Fahrenheit for most hair and lower for fine or colour-treated hair. "As hot as it goes" is the wrong answer.
- **Do you use a heat protectant?** Yes is the only answer.
- **How many passes?** One or two slow passes with a thin section. A stylist who goes over each piece five times is cooking it.

Add a scalp treatment or a trim if you need one; Ada's lists a scalp treatment at $25. A silk press is the best time to trim, because the ends are visible and straight.

## Before you go

Arrive with your hair as it is, in a protective style or loose. Do not flat-iron it yourself first, and do not put oil or butter in it the night before; it sits under the heat and that is where the sizzling smell comes from. Some stylists prefer you to arrive with hair washed; most want to do the wash themselves. The service listing or a quick message will say.

## The first night

The press is set when the hair cools. Do not put it up for the first few hours. That night, wrap it: brush the hair flat around the head, one direction, pin it, and tie a silk or satin scarf over it. In the morning unwrap, brush out and it falls back into place. If wrapping does not suit your length, a loose pineapple on top of the head in a silk scrunchie is the next best thing, with a satin pillowcase.

## The week after

A silk press lasts one to two weeks, until the next wash or the first serious humidity, whichever comes first.

- **Keep water away from it.** Shower caps, and a dry towel at hand. Sweat at the hairline is the usual enemy; a light sweatband at the gym and a quick blow-dry on cool afterwards buy a few days.
- **No more heat.** The temptation is to run a flat iron over the front every morning. Each pass is damage the press did not do. Wrap it instead, and if a piece has puffed, a little warmth from your palms and a brush does more than you think.
- **A light oil on the ends only,** a drop between the palms, if they look dry. Nothing on the roots.
- **Do not tie it tight.** A tight ponytail every day leaves a bend the press cannot undo and puts strain on the edges.

## What reverting should look like

At the next wash your hair should curl back as it was, everywhere. If the front or the ends stay straight when wet, that section has heat damage. It will not recover; it has to be cut as the healthy hair grows. One damaged press is a lesson, not a disaster. A pattern of them is a reason to change the heat, the stylist, or how often you press.

## How often

Every six to eight weeks is a comfortable rhythm for most hair. Between presses, wear your curls, a protective style, or braids; [Knotless braids: what to ask for](/journal/knotless-braids-what-to-ask-for) is the other half of a yearly plan many people settle into: press, braids, press.

## Booking on LogaLuxe

Stylists on LogaLuxe list a silk press with its time and price and show their free appointments. Put your hair length and whether you want a trim in the notes. [Find a hair stylist near you](/search?category=hair).$md$,
 '', 'hair', '{"silk press",hair,heat,aftercare,"natural hair"}', 'LogaLuxe editorial', 'Editorial team', '', 'published', timestamptz '2026-09-09 09:00+00', false, 10, 'hair', 'Find a hair stylist near you',
 $t$Silk press care: keep it sleek, keep your curls$t$,
 $t$What to ask a stylist about heat and passes, how to wrap it the first night, how to make it last the week, and what healthy reverting looks like.$t$,
 'seed');

-- Reading time the way the API computes it on save: words at 220 a minute, rounded, never under one.
update articles set reading_minutes = greatest(1, (array_length(regexp_split_to_array(trim(body_md), E'\\s+'), 1) + 110) / 220) where created_by = 'seed';

-- Covers: the three hero photos, each as a Journal picture for one article. The file in the bucket is shared.
with hero as (
  select storage_key, content_type, size_bytes, row_number() over (order by sort, created_at) as n from site_media where slot = 'hero' and active
), want as (
  select * from (values
    ('knotless-braids-what-to-ask-for', 1),
    ('what-a-first-visit-costs-in-nashville-and-lagos', 2),
    ('gel-or-acrylic-nails-which-to-book', 3)
  ) as v(slug, n)
), made as (
  insert into site_media (slot, ref, storage_key, content_type, size_bytes, alt, uploaded_by, caption, caption_pos, sort)
  select 'article', a.slug, h.storage_key, h.content_type, h.size_bytes, a.cover_alt, 'seed', '', 'none', 0
  from want w join hero h on h.n = w.n join articles a on a.slug = w.slug
  returning id, ref
)
update articles a set cover_media_id = made.id from made where a.slug = made.ref;
