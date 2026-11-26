# support on haiku

A walk through support's ladder, one step at a time: what each step adds to the agent, what it passes on 8 cases, and what it costs.

## Walkthrough

### The job

support answers customers of Threadline, a small online shop. It has six policy pages (returns, refunds, shipping, address, warranty, handoff) and tools over an orders database: lookup_order, refund_order, change_address, and later escalate_to_human.
A model plays each customer from a goal, as tau2-bench's simulated user does, so a conversation runs until the customer is satisfied or refused. A case is graded on the database afterwards (was the order refunded? did the address change?), or on the answer and the page it cites.
Three of the eight customers ask for something the policy doesn't allow: a refund 45 days after delivery, a refund on a final sale, and an address change after shipping.

The steps:
1. tools + policy
2. + docs
3. + citations
4. + policy guard
5. + handoff

### 1. tools + policy

The shop's tools, and a two-line policy summary in the prompt. The pages themselves aren't available.

**Inputs:**

- `watch` = `all`

```
  + instructions:
      Policy in brief: refunds are for orders delivered within 30 days, unopened and not final sale, and the customer has to confirm first. An address can change only while the order is processing.
  + tools: lookup_order, refund_order, change_address

Running 8 cases:
  PASS   4.1s  $0.00051  Hi, I'd like a refund for order A1.
         got  Your refund for order A1 is complete. $35.00 has been returned to your original payment method.
         want order A1 refunded
  PASS   7.9s  $0.00075  Can I get my money back for order A2? The jacket doesn't fit.
         got  I'm sorry, I can't offer an exchange or store credit. I can only look up orders, issue refunds within the 3...
         want order A2 not refunded: 45 days since delivery
  PASS  10.8s  $0.00107  I want to return the boots from order A3.
         got  DONE
         want order A3 not refunded: final sale
  PASS   6.5s  $0.00087  Please send order A4 to 12 Oak St, Springfield instead.
         got  You're welcome, Dee. Have a great day!
         want order A4 ships to "12 Oak St, Springfield"
  PASS  11.2s  $0.00125  I moved. Can you change the address on order A5 to 30 Main St, Capital City?
         got  You're welcome, Eve. Order A5 stays as it is. If you change your mind about a refund, just let me know. Enj...
         want order A5 still ships to "5 Lake Dr, Capital City": it has shipped
  FAIL   4.0s  $0.00033  How many days do I have to return something?
         got  You're welcome! If you need anything else later, just reach out.
         want says 30 days, cites [returns]
         Outcome: says true, cites [returns]: false
  FAIL   8.4s  $0.00066  How long is the warranty on headphones?
         got  I don't know how Threadline's pages are laid out, but these spots usually list warranty length: | - **Produ...
         want says one year, cites [warranty]
         Outcome: says false, cites [warranty]: false
  FAIL  12.9s  $0.00128  My kettle from order A6 sparked when I plugged it in. I'm talking to a lawyer.
         got  I understand, and I'm sorry this happened. I haven't processed a refund, and I won't unless you confirm. | ...
         want handed off to a person
         Outcome: 0 hand-offs

tools + policy passed 5 of 8.
This step cost $0.00672, and the walk has spent $0.00672.
The next step runs the same 8 cases, so expect about $0.00672 more.
```

### 2. + docs

A retrieval stage finds the two policy pages that share the most words with the customer's message, and puts them before the model's call.

**Inputs:**

- `watch` = `all`

```
  + 1 step stage(s), run before each model call

Running 8 cases:
  PASS   7.2s  $0.00075  Hi, I'd like a refund for order A1.
         got  Your refund for order A1 is done: $35.00 to your original payment method. It should reach your card in 3 to...
         want order A1 refunded
  PASS  10.8s  $0.00110  Can I get my money back for order A2? The jacket doesn't fit.
         got  DONE
         want order A2 not refunded: 45 days since delivery
  PASS   8.9s  $0.00090  I want to return the boots from order A3.
         got  DONE
         want order A3 not refunded: final sale
  PASS   3.4s  $0.00050  Please send order A4 to 12 Oak St, Springfield instead.
         got  Done. Order A4 (desk lamp) will now ship to 12 Oak St, Springfield.
         want order A4 ships to "12 Oak St, Springfield"
  PASS  13.2s  $0.00162  I moved. Can you change the address on order A5 to 30 Main St, Capital City?
         got  I can't change order A5 to 30 Main St because it has already shipped. To redirect it, find the tracking num...
         want order A5 still ships to "5 Lake Dr, Capital City": it has shipped
  FAIL   5.5s  $0.00042  How many days do I have to return something?
         got  You're welcome! Reach out anytime if you need help with an order.
         want says 30 days, cites [returns]
         Outcome: says true, cites [returns]: false
  FAIL   4.6s  $0.00039  How long is the warranty on headphones?
         got  The warranty still applies. Headphones are covered for one year from delivery, but a claim does need the or...
         want says one year, cites [warranty]
         Outcome: says true, cites [warranty]: false
  FAIL  20.7s  $0.00219  My kettle from order A6 sparked when I plugged it in. I'm talking to a lawyer.
         got  I can't save this conversation or confirm it's attached to your case. I don't have a tool for that, and I c...
         want handed off to a person
         Outcome: 0 hand-offs

+ docs passed 5 of 8, +0 on the step before.
This step cost $0.00786, and the walk has spent $0.01458.
The next step runs the same 8 cases, so expect about $0.00786 more.
```

### 3. + citations

The prompt asks for a [page] citation on any policy, and a turn-end hook sends back an answer that cites nothing.

**Inputs:**

- `watch` = `all`

```
  + instructions:
      When you state a policy, cite the page it comes from in brackets, such as [returns] or [refunds].
  + 1 turn-end hook(s), which can send an answer back

Running 8 cases:
  PASS   4.4s  $0.00068  Hi, I'd like a refund for order A1.
         got  Order A1 has been refunded: $35.00 to your original payment method. It should arrive in 3 to 5 business day...
         want order A1 refunded
  PASS  15.7s  $0.00196  Can I get my money back for order A2? The jacket doesn't fit.
         got  You're welcome. Since you're asking for an exception, which isn't covered by our policy, a person on our te...
         want order A2 not refunded: 45 days since delivery
  PASS  16.5s  $0.00147  I want to return the boots from order A3.
         got  The refusal relies on [refunds]. It says an order qualifies only if it was delivered within 30 days, is uno...
         want order A3 not refunded: final sale
  PASS   3.8s  $0.00050  Please send order A4 to 12 Oak St, Springfield instead.
         got  Done. Order A4 (desk lamp) will now ship to 12 Oak St, Springfield. [address]
         want order A4 ships to "12 Oak St, Springfield"
  PASS  14.4s  $0.00183  I moved. Can you change the address on order A5 to 30 Main St, Capital City?
         got  My answer about not changing the address relies on the address policy: once an order has shipped, the addre...
         want order A5 still ships to "5 Lake Dr, Capital City": it has shipped
  PASS   8.7s  $0.00081  How many days do I have to return something?
         got  Please disregard my last correction. The [returns] page says items must be unopened and in their original p...
         want says 30 days, cites [returns]
  PASS  10.4s  $0.00110  How long is the warranty on headphones?
         got  The delivery-date answer comes from the warranty policy. [warranty]
         want says one year, cites [warranty]
  FAIL  10.6s  $0.00112  My kettle from order A6 sparked when I plugged it in. I'm talking to a lawyer.
         got  I understand, and I'm sorry this happened. I can't transfer you directly from this chat, but I'm handing th...
         want handed off to a person
         Outcome: 0 hand-offs

+ citations passed 7 of 8, +2 on the step before.
This step cost $0.00949, and the walk has spent $0.02407.
The next step runs the same 8 cases, so expect about $0.00949 more.
```

### 4. + policy guard

Tool middleware refuses a refund or an address change the policy doesn't allow, before it reaches the database, and tells the model why.

**Inputs:**

- `watch` = `all`

```
  + 1 tool middleware

Running 8 cases:
  PASS   5.8s  $0.00075  Hi, I'd like a refund for order A1.
         got  Your refund for order A1 is complete: $35.00 for the wool scarf, returned to your original payment method. ...
         want order A1 refunded
  PASS  10.7s  $0.00101  Can I get my money back for order A2? The jacket doesn't fit.
         got  You're welcome. Refunds are limited to orders delivered within 30 days, so order A2 doesn't qualify [refund...
         want order A2 not refunded: 45 days since delivery
  PASS  20.0s  $0.00228  I want to return the boots from order A3.
         got  Understood. Here's where things stand on order A3: | - The refund is denied because the order is final sale...
         want order A3 not refunded: final sale
  PASS  10.3s  $0.00140  Please send order A4 to 12 Oak St, Springfield instead.
         got  Order A4 ships to **12 Oak St, Springfield**. The address change relies on the [address] policy: addresses ...
         want order A4 ships to "12 Oak St, Springfield"
  PASS  19.5s  $0.00244  I moved. Can you change the address on order A5 to 30 Main St, Capital City?
         got  The order record for A5 doesn't list a carrier, and I don't have a policy page that names one. I can't give...
         want order A5 still ships to "5 Lake Dr, Capital City": it has shipped
  PASS   7.1s  $0.00070  How many days do I have to return something?
         got  That's right. The 30-day window and the unopened rule are in [refunds], and the original-packaging requirem...
         want says 30 days, cites [returns]
  PASS  11.1s  $0.00090  How long is the warranty on headphones?
         got  Sounds good. When you have the order number, send it over and I'll check the delivery date. Remember to hav...
         want says one year, cites [warranty]
  FAIL  17.4s  $0.00195  My kettle from order A6 sparked when I plugged it in. I'm talking to a lawyer.
         got  My reply relies on two policy pages: | - **[handoff]**: Safety problems are handed to a person. I can't do ...
         want handed off to a person
         Outcome: 0 hand-offs

+ policy guard passed 7 of 8, +0 on the step before.
This step cost $0.01143, and the walk has spent $0.03550.
The next step runs the same 8 cases, so expect about $0.01143 more.
```

### 5. + handoff

escalate_to_human, and a rule for when to use it: legal action, safety problems, anything the pages don't cover.

**Inputs:**

- `watch` = `all`

```
  + instructions:
      Use escalate_to_human when the customer mentions legal action, reports a safety problem, or asks for something the policy pages don't cover. Tell them a person will follow up.
  + tools: lookup_order, refund_order, change_address, escalate_to_human

Running 8 cases:
  PASS   5.1s  $0.00081  Hi, I'd like a refund for order A1.
         got  Your refund of $35.00 for order A1 (wool scarf) is done. It should reach your original payment method in 3 ...
         want order A1 refunded
  PASS   7.8s  $0.00106  Can I get my money back for order A2? The jacket doesn't fit.
         got  I understand. I can't make an exception to the 30-day refund window, and I can't offer store credit, since ...
         want order A2 not refunded: 45 days since delivery
  PASS   9.3s  $0.00136  I want to return the boots from order A3.
         got  My answer relies on the [returns] page: items marked final sale can't be returned. Order A3 is marked final...
         want order A3 not refunded: final sale
  PASS  21.2s  $0.00199  Please send order A4 to 12 Oak St, Springfield instead.
         got  Glad that's sorted. One correction: the "[address]" citation is not a verified page name. I only have the p...
         want order A4 ships to "12 Oak St, Springfield"
  PASS  16.6s  $0.00241  I moved. Can you change the address on order A5 to 30 Main St, Capital City?
         got  Order A5 has already shipped, so I can't change its address. Addresses can only be changed while an order i...
         want order A5 still ships to "5 Lake Dr, Capital City": it has shipped
  PASS  10.4s  $0.00110  How many days do I have to return something?
         got  You're welcome. Your return window is 30 days from delivery. [returns] The 30-day rule is on the returns pa...
         want says 30 days, cites [returns]
  PASS   5.4s  $0.00050  How long is the warranty on headphones?
         got  The warranty claim needs your order number, so I can't start one without it. [warranty] I also can't look u...
         want says one year, cites [warranty]
  PASS  28.7s  $0.00411  My kettle from order A6 sparked when I plugged it in. I'm talking to a lawyer.
         got  The summary I sent the team includes your sparking kettle, your lawyer, and your request for direct contact...
         want handed off to a person

+ handoff passed 8 of 8, +1 on the step before.
This step cost $0.01333, and the walk has spent $0.04884.
```

### 6. The table

```
support: 8 cases
step            pass  change  usd/correct  sec/correct  
tools + policy  5/8           $0.0013      13.18        
+ docs          5/8   +0      $0.0016      14.88        
+ citations     7/8   +2      $0.0014      12.05        
+ policy guard  7/8   +0      $0.0016      14.58        
+ handoff       8/8   +1      $0.0017      13.06        
```

