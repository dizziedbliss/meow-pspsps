# Booky: the Telegram booking bot

Build **Booky**, a Telegram bot that books appointments into a Google Calendar. Customers chat with it in plain language; Booky works out the date, checks the calendar and books the day if it's free.

## How the judge runs your workflow

- **Trigger:** a **Telegram Trigger** (updates: `message`). Reply with a Telegram **Send a text message** to the same chat.
- **Calendar:** use the **Google Calendar** nodes or tools (check events or availability, create events). Any calendar works.
- **AI:** any chat model you like. You'll enter **your own** API key for it when you run tests. Older Gemini models such as `gemini-2.5-flash-lite` are closed to new keys; pick a current one (e.g. `gemini-3.5-flash-lite`).
- The judge **simulates Telegram and Google Calendar**, so you don't paste anything for those. Build with your own bot and Google account; the judge swaps in its simulated ones automatically.
- **Timezone:** Asia/Kolkata. "Today" means today in India.

You may use any supported node layout and any wording that meets the requirements. The judge checks replies and calendar outcomes, not whether your workflow matches the reference.

## What Booky must do

1. **No date in the message:** say it's a booking assistant and ask which date they'd like. Don't book anything.
2. **Today:** same-day bookings aren't possible. Say so and ask for another day.
3. **A date in the past:** refuse (playfully is fine) and ask for a future date.
4. **A day that already has anything on it** (another booking, a holiday, any event): apologise and ask for another day. Don't book.
5. **A free future day:** create an event with the title `Appointment - <customer's Telegram first name>` on that date (all-day, or at the time they asked for). Only after it's created, confirm the booking and **mention the date** in a friendly format, like `Friday, 6 Nov 2026`.
6. **"What dates are free?"** Check the next 7 days and suggest the free ones (never today).
7. Understand normal ways of saying dates: `tomorrow`, `day after tomorrow`, `5th Nov`, `Friday, 6 Nov 2026`.
8. **Remember the conversation per chat.** If someone first says "I want an appointment" and then "the 14th works", that's a booking for the 14th.
9. Replies are short, friendly plain text: no markdown (`**`, `##`), no JSON, no talk about tools or steps, one message per reply.

## Scoring

2 sample tests show their full input and output. The hidden tests cover the rules above with fresh dates and names every run, so hardcoding answers won't work. Every run sends real messages through your AI model and spends your tokens. If the grading service is unavailable, the run is reported as a judge-side failure instead of receiving a score.
