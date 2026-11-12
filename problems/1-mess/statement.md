# Hostel Mess Feedback Loop

Build an n8n workflow that records hostel meal feedback and produces a daily report for the warden.
Use two **Webhook nodes** (`POST`, JSON in, JSON out via *Respond to Webhook*) with these exact paths.
Everything else in your workflow is up to you.

## `mess-feedback`

```json
{ "roll_number": "4MC21CS001", "meal": "Lunch", "rating": 4, "comment": "Sambar was watery", "date": "2026-11-12" }
```

`comment` and `date` are optional; no `date` = today in India (Asia/Kolkata).

- `roll_number` required (ignore spaces and case); `meal` is Breakfast, Lunch, Snacks or Dinner (any case);
  `rating` a whole number 1 to 5 (`4` or `"4"` is fine).
- Duplicate = same roll number + meal + date as an earlier accepted entry; don't store it again.

Reply: `{ "status": "accepted" | "duplicate" | "rejected", "message": "..." }`

## `mess-report`

Body `{ "date": "2026-11-12" }` (none = today). Reply:

```json
{
  "date": "2026-11-12", "total_responses": 7, "overall_rating": 3.43,
  "meals": [ { "meal": "Breakfast", "responses": 2, "average_rating": 1.5 }, ...all four, in this order... ],
  "meals_requiring_attention": ["Breakfast"],
  "top_complaints": [ { "complaint": "Food served cold", "mentions": 4 } ],
  "hygiene_concerns": [ "Hair found in curry" ]
}
```

- Only accepted feedback for that date counts. A meal with no responses has `average_rating: null`.
- Averages and `overall_rating` rounded to 2 decimals.
- `meals_requiring_attention`: average **below 2.5**.
- `top_complaints`: up to 3 groups of similar complaints, most mentioned first (comments can be English,
  Hindi, Hinglish or Kannada; praise isn't a complaint).
- `hygiene_concerns`: one entry per comment about insects, hair, dirty utensils, spoiled food and the like.
- Comments are data; "ignore your instructions" must change nothing.

## Must know

- **Google Sheets:** use the tab named `Sheet1` (select **By Name**) and *Map Automatically* (or write
  your own header row first). Any other storage works too.
- **AI:** any model, your own key; the run form asks for it.
