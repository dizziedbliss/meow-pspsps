# Practice: order summary

Build a workflow that receives an order and replies with a summary.

**Trigger:** a Webhook node, HTTP method `POST`, responding with a **Respond to Webhook** node.
(The judge sets the webhook path itself, so any path works.)

**Request body**

```json
{ "customer": "Asha", "items": [ { "name": "pen", "qty": 2, "price": 1.5 }, { "name": "book", "qty": 1, "price": 12 } ] }
```

**Response** (HTTP 200, JSON)

```json
{ "customer": "Asha", "item_count": 2, "total": 15, "most_expensive": "book" }
```

- `total` is the sum of `qty × price`, rounded to 2 decimals.
- `most_expensive` is the name of the item with the highest unit `price`.
- If `items` is missing or not an array, reply **HTTP 400** with `{ "error": "<any message>" }`.
- An empty `items` array is valid: `item_count` 0, `total` 0, `most_expensive` null.
