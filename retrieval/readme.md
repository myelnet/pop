# Retrieval Manager

This module manages a trustless transfer of bytes between 2 parties. It is based on go-fil-markets implementation
but does not require unsealing as the blocks are not sealed in storage sectors but ready to be delivered.


# Basic flow

1. Client calls stateMachines.begin with a new deal id and initial deal state

2. Client sends an EventOpen to the state machine 

3. The state machine moves to StatusNew deal sending a deal Proposal voucher and a data transfer pull request.

4. The Provider receives the voucher, runs some validation and sends back a response stating if they accept or not.

5. If the deal status is accepted, the client will get or create a new channel adding the funds in anticipation of the whole transfer costs.

6. The Client state machine will block until the payment channel is confirmed on chain

7. Once the channel address is returned the state machine will call the payment manager to allocate and return a new lane number


